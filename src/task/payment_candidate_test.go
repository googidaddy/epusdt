package task

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/request"
	"github.com/GMWalletApp/epusdt/model/service"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type candidateHeaderAPI struct{ header *types.Header }

func (a *candidateHeaderAPI) GetBlockByNumber(context.Context, string, bool) (*types.Header, error) {
	return a.header, nil
}

func TestFixedCandidateEVMBackfillPersistsAndPropagatesWriteFailure(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	address := "0x2222222222222222222222222222222222222222"
	if err := dao.Mdb.Create(&mdb.WalletAddress{Address: address, Network: mdb.NetworkBsc, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	resp, err := service.CreateTransaction(&request.CreateTransactionRequest{FixedPaymentIntent: true, OrderId: "backfill-candidate", Token: "USDT", Network: mdb.NetworkBsc,
		Currency: "CNY", Amount: 100, NotifyUrl: "https://93.184.216.34/notify"}, &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	terms := resp.PaymentTerms
	raw, _ := new(big.Int).SetString(terms.RawAmount, 10)
	server := rpc.NewServer()
	defer server.Stop()
	header := &types.Header{Number: big.NewInt(100), Time: uint64(terms.CreatedAtMs/1000 + 1), Difficulty: big.NewInt(1)}
	if err := server.RegisterName("eth", &candidateHeaderAPI{header: header}); err != nil {
		t.Fatal(err)
	}
	client := ethclient.NewClient(rpc.DialInProc(server))
	defer client.Close()
	event := types.Log{Address: common.HexToAddress(terms.Contract), Topics: []common.Hash{transferEventHash, {}, common.BytesToHash(common.HexToAddress(address).Bytes())},
		Data: raw.FillBytes(make([]byte, 32)), BlockNumber: 100, BlockHash: header.Hash(), TxHash: common.HexToHash("0xabc"), Index: 4}
	watch := func(common.Address) bool { return true }
	// A log provider cannot substitute a block hash that the separate header
	// provider does not recognize, even if all payment fields match.
	conflicting := event
	conflicting.BlockHash = common.HexToHash("0xdeadbeef")
	if err := processEvmBackfillLogs(context.Background(), splitBackfillRPC{headers: client, logs: client}, mdb.NetworkBsc, "test", []types.Log{conflicting}, watch); err == nil {
		t.Fatal("conflicting provider block hash accepted")
	}
	if rows, err := data.ListPaymentCandidates(1); err != nil || len(rows) != 0 {
		t.Fatal("conflicting header/log evidence persisted")
	}
	if err := processEvmBackfillLogs(context.Background(), client, mdb.NetworkBsc, "test", []types.Log{event}, watch); err != nil {
		t.Fatal(err)
	}
	rows, err := data.ListPaymentCandidates(1)
	if err != nil || len(rows) != 1 || rows[0].EventIndex == nil || *rows[0].EventIndex != 4 {
		t.Fatal("backfill lost event identity")
	}
	order, err := data.GetOrderInfoByTradeId(terms.TradeID)
	if err != nil || order.Status != mdb.StatusWaitPay {
		t.Fatal("backfill settled candidate")
	}
	if err := dao.Mdb.Migrator().DropTable(&mdb.PaymentCandidate{}); err != nil {
		t.Fatal(err)
	}
	err = processEvmBackfillLogs(context.Background(), client, mdb.NetworkBsc, "test", []types.Log{event}, watch)
	if err == nil || !strings.Contains(err.Error(), "persist candidate") {
		t.Fatalf("backfill swallowed candidate write failure: %v", err)
	}
}

func TestFixedCandidateTronCheckpointRetriesAfterWriteFailureAndRestart(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	address := "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV"
	if _, err := data.AddWalletAddress(address); err != nil {
		t.Fatal(err)
	}
	resp, err := service.CreateTransaction(&request.CreateTransactionRequest{FixedPaymentIntent: true, OrderId: "candidate-checkpoint", Token: "USDT", Network: mdb.NetworkTron,
		Currency: "CNY", Amount: 100, NotifyUrl: "https://93.184.216.34/notify"}, &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	terms := resp.PaymentTerms
	contractHex, err := TronAddressToHex(terms.Contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := new(big.Int).SetString(terms.RawAmount, 10)
	block := map[string]interface{}{"blockID": strings.Repeat("b", 64), "block_header": map[string]interface{}{"raw_data": map[string]interface{}{"number": 2, "timestamp": terms.CreatedAtMs}},
		"transactions": []interface{}{map[string]interface{}{"txID": strings.Repeat("a", 64), "ret": []interface{}{map[string]interface{}{"contractRet": "SUCCESS"}},
			"raw_data": map[string]interface{}{"contract": []interface{}{map[string]interface{}{"type": "TriggerSmartContract", "parameter": map[string]interface{}{"value": map[string]interface{}{
				"contract_address": contractHex, "owner_address": "41" + strings.Repeat("2", 40), "data": "a9059cbb" + strings.Repeat("0", 24) + strings.Repeat("1", 40) + fmt.Sprintf("%064x", raw)}}}}}}}}
	receipt := []interface{}{map[string]interface{}{"id": strings.Repeat("a", 64), "blockNumber": 2, "blockTimeStamp": terms.CreatedAtMs, "log": []interface{}{map[string]interface{}{"address": contractHex[2:], "topics": []string{transferEventHash.Hex()[2:], strings.Repeat("0", 64), strings.Repeat("0", 24) + strings.Repeat("1", 40)}, "data": fmt.Sprintf("%064x", raw)}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "gettransactioninfobyblocknum") {
			_ = json.NewEncoder(w).Encode(receipt)
			return
		}
		_ = json.NewEncoder(w).Encode(block)
	}))
	defer server.Close()
	node := &mdb.RpcNode{Network: mdb.NetworkTron, Url: server.URL, Type: mdb.RpcNodeTypeHttp, Weight: 1, Enabled: true, Purpose: mdb.RpcNodePurposeGeneral, Status: mdb.RpcNodeStatusOk}
	if err := dao.Mdb.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	if err := data.SaveTronScanCheckpoint(1); err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner()
	if err := scanner.Init(); err != nil {
		t.Fatal(err)
	}
	if scanner.lastBlock != 1 {
		t.Fatal("restart skipped saved checkpoint")
	}
	if err := dao.Mdb.Migrator().DropTable(&mdb.PaymentCandidate{}); err != nil {
		t.Fatal(err)
	}
	scanner.poll()
	checkpoint, err := data.GetTronScanCheckpoint()
	if err != nil || checkpoint.LastBlock != 1 || scanner.lastBlock != 1 {
		t.Fatal("candidate write failure advanced scan progress")
	}
	if err := dao.Mdb.AutoMigrate(&mdb.PaymentCandidate{}); err != nil {
		t.Fatal(err)
	}
	restarted := NewScanner()
	if err := restarted.Init(); err != nil {
		t.Fatal(err)
	}
	restarted.poll()
	rows, err := data.ListPaymentCandidates(1)
	if err != nil || len(rows) != 1 || rows[0].Kind != "tron_log" {
		t.Fatalf("candidate not recovered after restart: %v %#v", err, rows)
	}
	checkpoint, err = data.GetTronScanCheckpoint()
	if err != nil || checkpoint.LastBlock != 2 {
		t.Fatal("successful block not checkpointed")
	}
	order, err := data.GetOrderInfoByTradeId(terms.TradeID)
	if err != nil || order.Status != mdb.StatusWaitPay {
		t.Fatal("scanner treated calldata as successful payment")
	}
	// If a checkpoint is lost while order history exists, do not silently start at head.
	if err := dao.Mdb.Where("network = ?", mdb.NetworkTron).Delete(&mdb.TronScanCheckpoint{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewScanner().Init(); err == nil {
		t.Fatal("missing checkpoint silently skipped historical fixed orders")
	}
}

func TestTronReceiptDiscoveryIncludesIndirectCallsAndFailsOnMissingReceipts(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	address := "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV"
	if _, err := data.AddWalletAddress(address); err != nil {
		t.Fatal(err)
	}
	response, err := service.CreateTransaction(&request.CreateTransactionRequest{FixedPaymentIntent: true, OrderId: "indirect-tron", Token: "USDT", Network: mdb.NetworkTron, Currency: "CNY", Amount: 100, NotifyUrl: "https://93.184.216.34/notify"}, &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	terms := response.PaymentTerms
	raw, _ := new(big.Int).SetString(terms.RawAmount, 10)
	txID := strings.Repeat("a", 64)
	b := &Block{BlockID: strings.Repeat("b", 64), Transactions: []Transaction{{TxID: txID}}}
	b.BlockHeader.RawData.Number = 100
	b.BlockHeader.RawData.Timestamp = terms.CreatedAtMs + 1
	// No top-level token call at all: only the receipt identifies the transfer.
	payload, _ := json.Marshal([]interface{}{map[string]interface{}{"id": txID, "blockNumber": 100, "blockTimeStamp": terms.CreatedAtMs + 1, "log": []interface{}{map[string]interface{}{"address": "a614f803b6fd780986a42c78ec9c7f77e6ded13c", "topics": []string{transferEventHash.Hex()[2:], strings.Repeat("0", 64), strings.Repeat("0", 24) + strings.Repeat("1", 40)}, "data": fmt.Sprintf("%064x", raw)}}}})
	if err := processTronReceipts(b, payload); err != nil {
		t.Fatal(err)
	}
	if err := processTronReceipts(b, payload); err != nil {
		t.Fatal(err)
	}
	rows, err := data.ListPaymentCandidates(1)
	if err != nil || len(rows) != 1 || rows[0].EventIndex == nil || *rows[0].EventIndex != 0 {
		t.Fatal("indirect receipt lost or duplicated", err)
	}
	if processTronReceipts(b, []byte(`[]`)) == nil {
		t.Fatal("missing receipt accepted")
	}
	b.Transactions = append(b.Transactions, Transaction{TxID: strings.Repeat("c", 64)})
	if processTronReceipts(b, payload) == nil {
		t.Fatal("partial receipt page accepted")
	}
	order, err := data.GetOrderInfoByTradeId(terms.TradeID)
	if err != nil || order.Status != mdb.StatusWaitPay {
		t.Fatal("discovery settled payment")
	}
	if err := dao.Mdb.Where("address = ?", address).Delete(&mdb.WalletAddress{}).Error; err != nil {
		t.Fatal(err)
	}
	wallets, err := data.GetScanWallets(mdb.NetworkTron)
	if err != nil || len(wallets) != 1 || wallets[0].Address != address {
		t.Fatal("deleted address lost history")
	}
}
