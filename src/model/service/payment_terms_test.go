package service

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/request"
	"github.com/spf13/viper"
)

func fixedOrderFixture(t *testing.T) (*mdb.Orders, *request.CreateTransactionRequest, *mdb.ApiKey) {
	t.Helper()
	address, err := tronHexToAddress("412222222222222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.AddWalletAddress(address); err != nil {
		t.Fatal(err)
	}
	// Synthetic test recipient; no network request is made by order creation.
	req := newCreateTransactionRequest("fixed-intent", 100)
	req.FixedPaymentIntent = true
	key := &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 7}}
	resp, err := CreateTransaction(req, key)
	if err != nil {
		t.Fatal(err)
	}
	order, err := data.GetOrderInfoByTradeId(resp.TradeId)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentTerms == nil || resp.PaymentTerms == nil {
		t.Fatal("missing persisted payment terms")
	}
	return order, req, key
}

func TestFixedPaymentRetryKeepsOriginalTerms(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, req, key := fixedOrderFixture(t)
	before := *order.PaymentTerms
	viper.Set("order_expiration_time", 90)
	if err := data.SetSetting("system", "system.amount_precision", "6", "number"); err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Model(&mdb.ChainToken{}).Where("network = ? AND symbol = ?", order.Network, order.Token).
		Updates(map[string]interface{}{"decimals": 18, "contract_address": "changed", "enabled": false}).Error; err != nil {
		t.Fatal(err)
	}
	rsp, err := CreateTransaction(req, key)
	if err != nil {
		t.Fatal(err)
	}
	if rsp.TradeId != order.TradeId || *rsp.PaymentTerms != before || rsp.ExpirationTime != before.ExpiresAtMs/1000 {
		t.Fatal("retry changed payment terms")
	}
	token, err := paymentTokenForVerification(order)
	if err != nil || token.ContractAddress != before.Contract || token.Decimals != before.Decimals {
		t.Fatalf("mutable configuration changed old token: %v", err)
	}
	if buildCheckoutResponse(order).ExpirationTime != before.ExpiresAtMs {
		t.Fatal("cashier extended expiry")
	}
	for _, modify := range []func(*request.CreateTransactionRequest){
		func(r *request.CreateTransactionRequest) { r.Amount++ },
		func(r *request.CreateTransactionRequest) { r.Network = mdb.NetworkBsc },
		func(r *request.CreateTransactionRequest) { r.NotifyUrl = "https://93.184.216.34/other" },
	} {
		changed := *req
		modify(&changed)
		if _, err := CreateTransaction(&changed, key); err == nil {
			t.Fatal("conflicting retry accepted")
		}
	}
	if _, err := CreateTransaction(req, &mdb.ApiKey{BaseModel: mdb.BaseModel{ID: 8}}); err == nil {
		t.Fatal("another merchant recovered order")
	}
	if _, err := SwitchNetwork(&request.SwitchNetworkRequest{TradeId: order.TradeId, Token: "USDT", Network: mdb.NetworkBsc}); err == nil {
		t.Fatal("fixed order switched network")
	}
}

func TestFixedPaymentWindowAmountAndOwnership(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, _, _ := fixedOrderFixture(t)
	terms := order.PaymentTerms
	raw, _ := new(big.Int).SetString(terms.RawAmount, 10)
	if err := validateFixedTransferCandidate(order, terms.Contract, raw, terms.CreatedAtMs); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []int64{0, terms.CreatedAtMs - 1, terms.ExpiresAtMs, terms.ExpiresAtMs + 1} {
		if err := validateFixedTransferCandidate(order, terms.Contract, raw, ts); err == nil {
			t.Fatalf("out-of-window transfer accepted at %d", ts)
		}
	}
	for _, diff := range []int64{-1, 1} {
		if err := validateFixedTransferCandidate(order, terms.Contract, new(big.Int).Add(raw, big.NewInt(diff)), terms.CreatedAtMs); err == nil {
			t.Fatal("nonexact transfer accepted")
		}
	}
	if err := validateFixedTransferCandidate(order, "wrong contract", raw, terms.CreatedAtMs); err == nil {
		t.Fatal("wrong token accepted")
	}
	// Confirmation may arrive later: the inclusion time, not verifier wall time, controls the window.
	if err := validatePaymentWindow(order, terms.ExpiresAtMs-1); err != nil {
		t.Fatal(err)
	}
	old := *order
	old.ID = 0
	old.TradeId = "historical"
	old.OrderId = "historical"
	old.PaymentTerms = nil
	old.PaymentExpiresAtMs = 0
	old.Status = mdb.StatusExpired
	if err := dao.Mdb.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Delete(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateFixedTransferCandidate(order, terms.Contract, raw, terms.CreatedAtMs); err == nil {
		t.Fatal("soft-deleted old order ownership was ignored")
	}
}

func TestFixedPaymentRejectsChangedOrderAndUsesFixedExpiry(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, _, _ := fixedOrderFixture(t)
	changed := *order
	changed.ActualAmount++
	if err := validatePaymentTerms(&changed); err == nil {
		t.Fatal("changed amount accepted")
	}
	changed = *order
	changed.ReceiveAddress = "other"
	if err := validatePaymentTerms(&changed); err == nil {
		t.Fatal("changed recipient accepted")
	}
	// A global shortened timeout must not prematurely expire this fixed order.
	updated, err := data.UpdateOrderIsExpirationById(order.ID, time.Now().Add(time.Hour))
	if err != nil || updated {
		t.Fatalf("global cutoff expired fixed order: %v", err)
	}
	if err := dao.Mdb.Model(order).Update("payment_expires_at_ms", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	updated, err = data.UpdateOrderIsExpirationById(order.ID, time.Now().Add(-24*time.Hour))
	if err != nil || !updated {
		t.Fatalf("fixed expiry was ignored: %v", err)
	}
}

func TestFixedPaymentConcurrentRetryCreatesOneOrder(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	order, req, key := fixedOrderFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rsp, err := CreateTransaction(req, key)
			if err != nil {
				t.Error(err)
				return
			}
			if rsp.TradeId != order.TradeId {
				t.Error("duplicate intent")
			}
		}()
	}
	wg.Wait()
	var count int64
	if err := dao.Mdb.Model(&mdb.Orders{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("order count=%d err=%v", count, err)
	}
}

func TestFixedPaymentDoesNotReuseExpiredAllocation(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	old, req, key := fixedOrderFixture(t)
	if err := dao.Mdb.Model(old).Update("status", mdb.StatusExpired).Error; err != nil {
		t.Fatal(err)
	}
	if err := data.UnLockTransactionByTradeId(old.TradeId); err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Delete(old).Error; err != nil {
		t.Fatal(err)
	}
	next := *req
	next.OrderId = "next-fixed"
	rsp, err := CreateTransaction(&next, key)
	if err != nil {
		t.Fatal(err)
	}
	if rsp.ActualAmount == old.ActualAmount {
		t.Fatal("soft-deleted expired allocation reused")
	}
	current, err := data.GetOrderInfoByTradeId(rsp.TradeId)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.EnsureUniquePaymentOwner(current); err != nil {
		t.Fatal(err)
	}
	token, err := paymentTokenForVerification(current)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := new(big.Int).SetString(current.PaymentTerms.RawAmount, 10)
	TryProcessTronTRC20Transfer(*token, current.ReceiveAddress, raw, "synthetic-late", current.PaymentTerms.ExpiresAtMs)
	current, err = data.GetOrderInfoByTradeId(rsp.TradeId)
	if err != nil || current.Status != mdb.StatusWaitPay {
		t.Fatalf("late candidate changed order status: %v", err)
	}
	current.PaymentTerms = nil
	if err := validatePaymentTerms(current); err == nil {
		t.Fatal("missing snapshot downgraded to legacy")
	}
}
