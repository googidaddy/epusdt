package service

import (
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/request"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
)

func TestFixedCandidatesPersistReplayAndNeverSettle(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, _, _ := fixedOrderFixture(t)
	terms := order.PaymentTerms
	observation := mdb.PaymentCandidate{Network: mdb.NetworkTron, Kind: "tron_calldata", TransactionHash: strings.Repeat("a", 64),
		BlockHash: strings.Repeat("b", 64), BlockNumber: 100, BlockTimeMs: terms.CreatedAtMs, Contract: terms.Contract, Recipient: terms.Recipient, RawAmount: terms.RawAmount}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handled, err := RecordFixedTransferObservation(observation)
			if !handled && err == nil {
				err = fmt.Errorf("not captured")
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := data.ListPaymentCandidates(order.ApiKeyID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("replay duplicated/lost observation: %v %#v", err, rows)
	}
	if rows[0].ReviewReason != "independent_check_required" || rows[0].EventIndex != nil {
		t.Fatal("calldata presented as verified receipt event")
	}
	if err := OrderProcessing(&request.OrderProcessingRequest{TradeId: order.TradeId, BlockTransactionId: observation.TransactionHash}); err == nil {
		t.Fatal("fixed candidate settled without independent proof")
	}
	stored, err := data.GetOrderInfoByTradeId(order.TradeId)
	if err != nil || stored.Status != mdb.StatusWaitPay || stored.BlockTransactionId != "" {
		t.Fatal("candidate changed payment state")
	}
	if callbackOrders, err := data.GetPendingCallbackOrders(10, 100); err != nil || len(callbackOrders) != 0 {
		t.Fatal("candidate queued a success callback")
	}
	// Reopen the on-disk primary DB, not a second in-memory copy.
	var database struct {
		Seq  int
		Name string
		File string
	}
	if err := dao.Mdb.Raw("PRAGMA database_list").Scan(&database).Error; err != nil {
		t.Fatal(err)
	}
	original := dao.Mdb
	sqlDB, _ := original.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := gorm.Open(sqlite.Open(database.File), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dao.Mdb = reopened
	defer func() { db, _ := reopened.DB(); _ = db.Close(); dao.Mdb = original }()
	rows, err = data.ListPaymentCandidates(order.ApiKeyID)
	if err != nil || len(rows) != 1 {
		t.Fatal("observation lost across reopen")
	}
	ids := []uint64{rows[0].ID}
	if err := data.AcknowledgePaymentCandidates(order.ApiKeyID+1, ids); err == nil {
		t.Fatal("another merchant acknowledged candidate")
	}
	for i := 0; i < 2; i++ {
		if err := data.AcknowledgePaymentCandidates(order.ApiKeyID, ids); err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := data.ListPaymentCandidates(order.ApiKeyID); err != nil || len(rows) != 0 {
		t.Fatal("ack not persisted")
	}
	if _, err := RecordFixedTransferObservation(observation); err != nil {
		t.Fatal(err)
	}
	if rows, err := data.ListPaymentCandidates(order.ApiKeyID); err != nil || len(rows) != 0 {
		t.Fatal("duplicate resurrected acknowledged row")
	}
	var count int64
	_ = dao.Mdb.Model(&mdb.PaymentCandidate{}).Count(&count).Error
	if count != 1 {
		t.Fatal("ack deleted history")
	}
}

func TestFixedCandidatesRetainLateAmbiguousAndRemovedEvidence(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, _, _ := fixedOrderFixture(t)
	terms := order.PaymentTerms
	observation := mdb.PaymentCandidate{Network: mdb.NetworkTron, Kind: "tron_calldata", TransactionHash: strings.Repeat("a", 64), BlockHash: strings.Repeat("b", 64),
		BlockNumber: 100, BlockTimeMs: terms.ExpiresAtMs, Contract: terms.Contract, Recipient: terms.Recipient, RawAmount: terms.RawAmount}
	// Runtime reservations are not an ownership ledger; expiry must not hide candidates.
	if err := data.UnLockTransactionByTradeId(order.TradeId); err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Model(order).Update("status", mdb.StatusExpired).Error; err != nil {
		t.Fatal(err)
	}
	if handled, err := RecordFixedTransferObservation(observation); !handled || err != nil {
		t.Fatalf("late observation lost: %v", err)
	}
	rows, _ := data.ListPaymentCandidates(order.ApiKeyID)
	if len(rows) != 1 || rows[0].ReviewReason != "late" {
		t.Fatal("late payment not isolated for review")
	}
	copy := *order
	copy.ID = 0
	copy.TradeId = "other-history"
	copy.OrderId = "other-history"
	copy.PaymentTerms = nil
	copy.PaymentExpiresAtMs = 0
	if err := dao.Mdb.Create(&copy).Error; err != nil {
		t.Fatal(err)
	}
	observation.BlockTimeMs = terms.CreatedAtMs
	if _, err := RecordFixedTransferObservation(observation); err != nil {
		t.Fatal(err)
	}
	rows, _ = data.ListPaymentCandidates(order.ApiKeyID)
	if len(rows) != 2 || rows[1].ReviewReason != "ambiguous" {
		t.Fatal("ambiguous history was auto-selected")
	}
	observation.Removed = true
	if _, err := RecordFixedTransferObservation(observation); err != nil {
		t.Fatal(err)
	}
	rows, _ = data.ListPaymentCandidates(order.ApiKeyID)
	if len(rows) != 3 || rows[2].ReviewReason != "removed" {
		t.Fatal("removal overwrote or erased earlier observation")
	}
	if err := dao.Mdb.Migrator().DropTable(&mdb.PaymentCandidate{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordFixedTransferObservation(observation); err == nil {
		t.Fatal("write failure swallowed")
	}
}

func TestFixedEVMCandidateKeepsLogIdentity(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	address := "0x2222222222222222222222222222222222222222"
	if err := dao.Mdb.Create(&mdb.WalletAddress{Address: address, Network: mdb.NetworkBsc, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	req := newCreateTransactionRequest("candidate-bsc", 100)
	req.FixedPaymentIntent = true
	req.Network = mdb.NetworkBsc
	resp, err := CreateTransaction(req, &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 7}})
	if err != nil {
		t.Fatal(err)
	}
	terms := resp.PaymentTerms
	raw, _ := new(big.Int).SetString(terms.RawAmount, 10)
	event := types.Log{Address: common.HexToAddress(terms.Contract), Topics: []common.Hash{common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"), {}, common.BytesToHash(common.HexToAddress(address).Bytes())},
		Data: raw.FillBytes(make([]byte, 32)), BlockNumber: 100, BlockHash: common.HexToHash("0xabc"), TxHash: common.HexToHash("0xdef"), Index: 3}
	if handled, err := RecordFixedEVMLog(mdb.NetworkBsc, event, 0); !handled || err != nil {
		t.Fatalf("missing timestamp discarded candidate: %v", err)
	}
	rows, _ := data.ListPaymentCandidates(7)
	if len(rows) != 1 || rows[0].ReviewReason != "missing_block_time" || rows[0].EventIndex == nil || *rows[0].EventIndex != 3 {
		t.Fatal("log identity/timestamp not retained")
	}
	event.Removed = true
	if _, err := RecordFixedEVMLog(mdb.NetworkBsc, event, terms.CreatedAtMs); err != nil {
		t.Fatal(err)
	}
	rows, _ = data.ListPaymentCandidates(7)
	if len(rows) != 2 || !rows[1].Removed {
		t.Fatal("removed log ignored")
	}
	event.Data = event.Data[:31]
	if _, err := RecordFixedEVMLog(mdb.NetworkBsc, event, terms.CreatedAtMs); err == nil {
		t.Fatal("malformed event accepted")
	}
}
