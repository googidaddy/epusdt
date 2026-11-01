package task

import (
	"context"
	"crypto/tls"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/util/securehttp"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gorilla/websocket"
)

var secureEndpoints map[string]bool
var secureBSCHeaderEndpoint string

// Headers and logs may come from different providers. Canonical log hashes and
// persisted checkpoints are still checked by the backfill loop; either source
// failing stops progress. This never changes the merchant's two-source proof.
type evmBackfillRPC interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	ChainID(context.Context) (*big.Int, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
}

type splitBackfillRPC struct {
	headers evmBackfillRPC
	logs    evmBackfillRPC
}

func (c splitBackfillRPC) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return c.headers.HeaderByNumber(ctx, number)
}
func (c splitBackfillRPC) FilterLogs(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error) {
	return c.logs.FilterLogs(ctx, query)
}
func (c splitBackfillRPC) ChainID(ctx context.Context) (*big.Int, error) {
	logsID, err := c.logs.ChainID(ctx)
	if err != nil || logsID == nil || logsID.Cmp(big.NewInt(56)) != 0 {
		return nil, errors.New("BSC log source identity unavailable")
	}
	headersID, err := c.headers.ChainID(ctx)
	if err != nil || headersID == nil || headersID.Cmp(logsID) != 0 {
		return nil, errors.New("BSC header source identity unavailable")
	}
	return logsID, nil
}

func dialBackfillEVM(ctx context.Context, endpoint, network string) (evmBackfillRPC, func(), error) {
	logs, err := dialEVM(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}
	if network != mdb.NetworkBsc || secureBSCHeaderEndpoint == "" || secureBSCHeaderEndpoint == endpoint {
		return logs, logs.Close, nil
	}
	headers, err := dialEVM(ctx, secureBSCHeaderEndpoint)
	if err != nil {
		logs.Close()
		return nil, nil, safeRPCError(err)
	}
	return splitBackfillRPC{headers: headers, logs: logs}, func() { headers.Close(); logs.Close() }, nil
}

// HTTP clients wrap transport errors with the full URL, including RPC API keys.
// Remote JSON-RPC error messages are also untrusted. Only legacy mode exposes
// those details; the secure profile records a fixed failure category.
func safeRPCError(err error) error {
	if err != nil && secureEndpoints != nil {
		return errors.New("configured RPC unavailable or denied")
	}
	return err
}

// ConfigureSecureRPC must be called once, before starting scanner goroutines.
//
// Both lists are ordered: index 0 is the operator's primary endpoint and the
// rest are reviewed failovers. The allowlist is the union of exactly these
// values — nothing is derived at runtime from rpc_nodes, so a database row
// cannot widen the SSRF surface on its own.
func ConfigureSecureRPC(tron []string, bscHTTP []string, bscWSS, bscHeaderHTTP string) error {
	if len(tron) == 0 || len(bscHTTP) == 0 {
		return securehttp.ErrDenied
	}
	endpoints := make([]string, 0, len(tron)*3)
	for _, base := range tron {
		if strings.TrimSpace(base) == "" {
			return securehttp.ErrDenied
		}
		endpoints = append(endpoints,
			base+"/walletsolidity/getnowblock",
			base+"/walletsolidity/getblockbynum",
			base+"/wallet/gettransactioninfobyblocknum")
	}
	client, err := securehttp.Client(endpoints...)
	if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(bscHTTP)+2)
	for _, raw := range bscHTTP {
		if _, err := securehttp.Endpoint(raw); err != nil {
			return err
		}
		allowed[raw] = true
	}
	ws, err := url.Parse(bscWSS)
	if err != nil || ws.Scheme != "wss" {
		return securehttp.ErrDenied
	}
	ws.Scheme = "https"
	if _, err := securehttp.Endpoint(ws.String()); err != nil {
		return err
	}
	allowed[bscWSS] = true
	if bscHeaderHTTP != "" {
		if _, err := securehttp.Endpoint(bscHeaderHTTP); err != nil {
			return err
		}
		allowed[bscHeaderHTTP] = true
	}
	httpClient = client
	secureEndpoints = allowed
	secureBSCHeaderEndpoint = bscHeaderHTTP
	return nil
}
func dialEVM(ctx context.Context, endpoint string) (*ethclient.Client, error) {
	if secureEndpoints == nil {
		return ethclient.DialContext(ctx, endpoint)
	}
	if !secureEndpoints[endpoint] {
		return nil, securehttp.ErrDenied
	}
	opts := []rpc.ClientOption{rpc.WithWebsocketMessageSizeLimit(4 << 20)}
	if strings.HasPrefix(endpoint, "https://") {
		client, err := securehttp.Client(endpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, rpc.WithHTTPClient(client))
	} else {
		opts = append(opts, rpc.WithWebsocketDialer(websocket.Dialer{NetDialContext: securehttp.Dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, HandshakeTimeout: 10 * time.Second, Proxy: nil}))
	}
	client, err := rpc.DialOptions(ctx, endpoint, opts...)
	if err != nil {
		return nil, securehttp.ErrDenied
	}
	return ethclient.NewClient(client), nil
}
