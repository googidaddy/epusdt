//go:build rpcpreflight

package task

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

// Explicit operator-only read-only probe. Never part of ordinary CI, never
// creates a database/order, and never prints the configuration or RPC errors.
func TestBSCProviderReadOnlyPreflight(t *testing.T) {
	path := os.Getenv("EPUSDT_RPC_PREFLIGHT_CONFIG")
	if path == "" {
		t.Fatal("explicit private preflight configuration required")
	}
	var config struct {
		Tron    string `json:"tron_url"`
		HTTP    string `json:"bsc_https"`
		WSS     string `json:"bsc_wss"`
		Headers string `json:"bsc_header_https"`
	}
	var contents []byte
	var err error
	if path == "-" {
		// SSH stdin avoids writing credentials into the remote temporary directory.
		contents, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	} else {
		stat, statErr := os.Stat(path)
		if statErr != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
			t.Fatal("preflight configuration must be a private regular file")
		}
		contents, err = os.ReadFile(path)
	}
	if err != nil || json.Unmarshal(contents, &config) != nil || config.Headers == "" || config.Headers == config.HTTP {
		t.Fatal("two separately configured providers required")
	}
	oldEndpoints, oldHeader, oldHTTP := secureEndpoints, secureBSCHeaderEndpoint, httpClient
	t.Cleanup(func() { secureEndpoints, secureBSCHeaderEndpoint, httpClient = oldEndpoints, oldHeader, oldHTTP })
	if ConfigureSecureRPC([]string{config.Tron}, []string{config.HTTP}, config.WSS, config.Headers) != nil {
		t.Fatal("invalid provider configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, closeClient, err := dialBackfillEVM(ctx, config.HTTP, mdb.NetworkBsc)
	if err != nil {
		t.Fatal("configured providers unavailable")
	}
	defer closeClient()
	id, err := client.ChainID(ctx)
	if err != nil || id == nil || id.Int64() != 56 {
		t.Fatal("provider chain identity disagreement")
	}
	head, err := client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil || head == nil || head.Number == nil || head.Number.Int64() < 10 {
		t.Fatal("finalized header unavailable")
	}
	from := new(big.Int).Sub(head.Number, big.NewInt(9))
	logs, err := client.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: from, ToBlock: head.Number,
		Addresses: []common.Address{common.HexToAddress("0x55d398326f99059ff775485246999027b3197955")}, Topics: [][]common.Hash{{transferEventHash}}})
	if err != nil || len(logs) == 0 {
		t.Fatal("ten-block Transfer sample unavailable")
	}
	event := logs[0]
	block, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(event.BlockNumber))
	if err != nil || block == nil || block.Number == nil || block.Number.Uint64() != event.BlockNumber || block.Hash() != event.BlockHash || event.Removed || event.BlockNumber > head.Number.Uint64() {
		t.Fatal("Go-decoded canonical header disagrees with finalized log evidence")
	}
	t.Log("independent provider chain IDs, finalized range and Go-decoded header/log hash agree; no database, order or payment was created")
}
