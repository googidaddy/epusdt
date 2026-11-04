package task

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type recoveryRPC struct {
	chain       hexutil.Uint64
	changed     bool
	failLogs    bool
	latestCalls int
	ranges      [][2]int64
}

func recoveryHeader(n int64) *types.Header {
	return &types.Header{Number: big.NewInt(n), Time: 1800000000 + uint64(n), Difficulty: big.NewInt(1)}
}
func (a *recoveryRPC) ChainId(context.Context) (hexutil.Uint64, error) { return a.chain, nil }
func (a *recoveryRPC) GetBlockByNumber(_ context.Context, tag string, _ bool) (*types.Header, error) {
	if tag == "latest" {
		a.latestCalls++
		if a.latestCalls > 2 {
			return nil, errors.New("synthetic end of polling")
		}
		return recoveryHeader(122), nil
	}
	if tag == "finalized" {
		return recoveryHeader(120), nil
	}
	n, err := hexutil.DecodeBig(tag)
	if err != nil {
		return nil, err
	}
	h := recoveryHeader(n.Int64())
	if a.changed && n.Int64() == 100 {
		h.Extra = []byte("different canonical block")
	}
	return h, nil
}
func (a *recoveryRPC) GetLogs(_ context.Context, q map[string]interface{}) ([]types.Log, error) {
	from, err := hexutil.DecodeBig(q["fromBlock"].(string))
	if err != nil {
		return nil, err
	}
	to, err := hexutil.DecodeBig(q["toBlock"].(string))
	if err != nil {
		return nil, err
	}
	a.ranges = append(a.ranges, [2]int64{from.Int64(), to.Int64()})
	if a.failLogs {
		return nil, errors.New("synthetic RPC unavailable")
	}
	return []types.Log{}, nil
}
func TestBSCFinalizedBackfillRestartAndFailClosed(t *testing.T) {
	for _, mode := range []string{"resume", "rpc_failure", "changed_checkpoint", "wrong_chain", "missing_checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			defer testutil.SetupTestDatabases(t)()
			if err := dao.Mdb.Model(&mdb.Chain{}).Where("network = ?", mdb.NetworkBsc).Updates(map[string]interface{}{"enabled": true, "scan_interval_sec": 1}).Error; err != nil {
				t.Fatal(err)
			}
			wallet := mdb.WalletAddress{Address: "0x2222222222222222222222222222222222222222", Network: mdb.NetworkBsc, Status: 1}
			if err := dao.Mdb.Create(&wallet).Error; err != nil {
				t.Fatal(err)
			}
			if mode != "missing_checkpoint" {
				if err := data.SaveScanProgress(mdb.NetworkBsc, 100, recoveryHeader(100).Hash().Hex()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := dao.Mdb.Create(&mdb.Orders{TradeId: "historical", OrderId: "historical", Network: mdb.NetworkBsc, PaymentTerms: &mdb.PaymentTerms{Version: 1}}).Error; err != nil {
					t.Fatal(err)
				}
			}
			a := &recoveryRPC{chain: 56, changed: mode == "changed_checkpoint", failLogs: mode == "rpc_failure"}
			if mode == "wrong_chain" {
				a.chain = 1
			}
			srv := rpc.NewServer()
			defer srv.Stop()
			if err := srv.RegisterName("eth", a); err != nil {
				t.Fatal(err)
			}
			client := ethclient.NewClient(rpc.DialInProc(srv))
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			err := runEvmBackfillLoop(ctx, client, mdb.NetworkBsc, "test", mdb.RpcNode{}, ethereum.FilterQuery{Addresses: []common.Address{common.HexToAddress("0x55d398326f99059ff775485246999027b3197955")}}, func(common.Address) bool { return true })
			state, e := data.GetScanHealth(mdb.NetworkBsc)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "resume":
				if len(a.ranges) != 2 || a.ranges[0] != [2]int64{101, 110} || a.ranges[1] != [2]int64{111, 120} || state.Scanned != 120 || state.ScannedHash != recoveryHeader(120).Hash().Hex() {
					t.Fatalf("lost restart cursor or exceeded finalized/ten-block window: %+v %+v", a.ranges, state)
				}
			case "missing_checkpoint":
				if err == nil || !strings.Contains(err.Error(), "checkpoint missing") || state.Scanned != 0 || len(a.ranges) != 0 {
					t.Fatal("missing historical cursor silently skipped", err)
				}
			default:
				if err == nil || state.Scanned != 100 || state.ScannedHash != recoveryHeader(100).Hash().Hex() {
					t.Fatal("failure advanced checkpoint", err)
				}
			}
		})
	}
}
