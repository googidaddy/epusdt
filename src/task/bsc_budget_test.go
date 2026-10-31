package task

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type budgetRPC struct {
	recoveryRPC
	headerOnly, logOnly   bool
	headerCalls, logCalls int
	failHeaders           bool
}

func (a *budgetRPC) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(int64(a.chain)), nil
}
func (a *budgetRPC) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	a.headerCalls++
	if a.logOnly || a.failHeaders {
		return nil, errors.New("synthetic header failure")
	}
	tag := "latest"
	if n != nil {
		if n.Int64() == -3 {
			tag = "finalized"
		} else {
			tag = "0x" + n.Text(16)
		}
	}
	return a.GetBlockByNumber(ctx, tag, false)
}
func (a *budgetRPC) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	a.logCalls++
	if a.headerOnly {
		return nil, errors.New("getLogs unavailable on header-only provider")
	}
	return a.GetLogs(ctx, map[string]interface{}{"fromBlock": "0x" + q.FromBlock.Text(16), "toBlock": "0x" + q.ToBlock.Text(16)})
}

func seedBudgetScan(t *testing.T) {
	t.Helper()
	if err := dao.Mdb.Model(&mdb.Chain{}).Where("network = ?", mdb.NetworkBsc).Updates(map[string]interface{}{"enabled": true, "scan_interval_sec": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Create(&mdb.WalletAddress{Address: "0x2222222222222222222222222222222222222222", Network: mdb.NetworkBsc, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := data.SaveScanProgress(mdb.NetworkBsc, 100, recoveryHeader(100).Hash().Hex()); err != nil {
		t.Fatal(err)
	}
}

func TestSplitBSCBackfillRoutingAndFailures(t *testing.T) {
	for _, mode := range []string{"resume", "wrong_header_chain", "wrong_log_chain", "header_failure", "log_failure", "changed_checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			defer testutil.SetupTestDatabases(t)()
			seedBudgetScan(t)
			headers := &budgetRPC{recoveryRPC: recoveryRPC{chain: 56, changed: mode == "changed_checkpoint"}, headerOnly: true, failHeaders: mode == "header_failure"}
			logs := &budgetRPC{recoveryRPC: recoveryRPC{chain: 56, failLogs: mode == "log_failure"}, logOnly: true}
			if mode == "wrong_header_chain" {
				headers.chain = 1
			}
			if mode == "wrong_log_chain" {
				logs.chain = 1
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			err := runEvmBackfillLoop(ctx, splitBackfillRPC{headers: headers, logs: logs}, mdb.NetworkBsc, "test", mdb.RpcNode{}, ethereum.FilterQuery{}, func(common.Address) bool { return true })
			state, e := data.GetScanHealth(mdb.NetworkBsc)
			if e != nil {
				t.Fatal(e)
			}
			if logs.headerCalls != 0 || headers.logCalls != 0 {
				t.Fatal("RPC methods crossed provider roles", logs.headerCalls, headers.logCalls)
			}
			if mode == "resume" {
				if len(logs.ranges) != 2 || logs.ranges[0] != [2]int64{101, 110} || logs.ranges[1] != [2]int64{111, 120} || state.Scanned != 120 {
					t.Fatalf("lost contiguous finalized ranges: %v %v", logs.ranges, state)
				}
			} else if err == nil || state.Scanned != 100 || state.ScannedHash != recoveryHeader(100).Hash().Hex() {
				t.Fatal("provider failure or disagreement advanced checkpoint", err, state)
			}
		})
	}
}

type partialRangeRPC struct {
	polls         int
	fill          bool
	ranges        [][2]int64
	firstLogAfter time.Duration
	started       time.Time
}

func (a *partialRangeRPC) ChainID(context.Context) (*big.Int, error) { return big.NewInt(56), nil }
func (a *partialRangeRPC) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if n == nil {
		if len(a.ranges) > 0 {
			return nil, errors.New("synthetic end")
		}
		a.polls++
		return recoveryHeader(112), nil
	}
	if n.Int64() == -3 {
		if a.fill && a.polls >= 2 {
			return recoveryHeader(110), nil
		}
		return recoveryHeader(105), nil
	}
	return recoveryHeader(n.Int64()), nil
}
func (a *partialRangeRPC) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	a.firstLogAfter = time.Since(a.started)
	a.ranges = append(a.ranges, [2]int64{q.FromBlock.Int64(), q.ToBlock.Int64()})
	return []types.Log{}, nil
}
func TestBSCPartialBatchFillsOrFlushesWithoutSkipping(t *testing.T) {
	for _, fill := range []bool{false, true} {
		t.Run(map[bool]string{false: "stalled_finalized_head", true: "range_fills"}[fill], func(t *testing.T) {
			defer testutil.SetupTestDatabases(t)()
			seedBudgetScan(t)
			a := &partialRangeRPC{fill: fill, started: time.Now()}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			_ = runEvmBackfillLoop(ctx, a, mdb.NetworkBsc, "test", mdb.RpcNode{}, ethereum.FilterQuery{}, func(common.Address) bool { return true })
			end := int64(105)
			if fill {
				end = 110
			}
			state, err := data.GetScanHealth(mdb.NetworkBsc)
			if err != nil || len(a.ranges) != 1 || a.ranges[0] != [2]int64{101, end} || state.Scanned != end || state.ScannedHash != recoveryHeader(end).Hash().Hex() {
				t.Fatalf("lost or prematurely advanced range: %v %v %v", a.ranges, state, err)
			}
			if a.firstLogAfter < 900*time.Millisecond || a.firstLogAfter > 3*time.Second {
				t.Fatal("partial range did not wait with a bounded deadline", a.firstLogAfter)
			}
		})
	}
}

func TestSecureHeaderEndpointValidationDoesNotPartiallyConfigure(t *testing.T) {
	oldEndpoints, oldHeader, oldHTTP := secureEndpoints, secureBSCHeaderEndpoint, httpClient
	t.Cleanup(func() { secureEndpoints, secureBSCHeaderEndpoint, httpClient = oldEndpoints, oldHeader, oldHTTP })
	secureEndpoints = nil
	secureBSCHeaderEndpoint = ""
	if err := ConfigureSecureRPC("https://tron.example.com", "https://logs.example.com/key", "wss://logs.example.com/key", "http://headers.example.com"); err == nil || secureEndpoints != nil || secureBSCHeaderEndpoint != "" {
		t.Fatal("invalid header endpoint partially enabled secure RPC")
	}
	if err := ConfigureSecureRPC("https://tron.example.com", "https://logs.example.com/key", "wss://logs.example.com/key", "https://headers.example.com"); err != nil {
		t.Fatal(err)
	}
	if secureBSCHeaderEndpoint != "https://headers.example.com" || !secureEndpoints[secureBSCHeaderEndpoint] {
		t.Fatal("header endpoint not pinned")
	}
	if _, err := dialEVM(context.Background(), "https://unconfigured.example.com"); err == nil {
		t.Fatal("unconfigured RPC accepted")
	}
}
