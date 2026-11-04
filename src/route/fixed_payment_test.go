package route

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
)

func TestFixedCandidateDeliveryIsMerchantScopedAndAckIsNotPayment(t *testing.T) {
	e := setupTestEnv(t)
	useRspErrorHTTPStatuses(e)
	key, err := data.GetEnabledApiKey("1")
	if err != nil || key.ID == 0 {
		t.Fatal("missing test key")
	}
	first := mdb.PaymentCandidate{ObservationKey: strings.Repeat("a", 64), APIKeyID: key.ID, TradeID: "owned", OrderID: "owned", ReviewReason: "independent_check_required"}
	other := mdb.PaymentCandidate{ObservationKey: strings.Repeat("b", 64), APIKeyID: key.ID + 100, TradeID: "other", OrderID: "other", ReviewReason: "late"}
	if err := data.SavePaymentCandidates([]mdb.PaymentCandidate{first, other}); err != nil {
		t.Fatal(err)
	}
	var rows []mdb.PaymentCandidate
	if err := dao.Mdb.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	path := "/payments/gmpay/v1/order/transfer-candidates"
	body := signBody(map[string]interface{}{"pid": "1", "action": "transfer_candidates.list.v1"})
	for i := 0; i < 2; i++ {
		rec := doPost(e, path, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %s", rec.Body.String())
		}
		found, ok := parseResp(t, rec)["data"].([]interface{})
		if !ok || len(found) != 1 {
			t.Fatal("candidate lost or another merchant disclosed")
		}
		row := found[0].(map[string]interface{})
		if row["trade_id"] != "owned" || row["id"] != fmt.Sprint(rows[0].ID) {
			t.Fatal("candidate response scope or ID encoding wrong")
		}
	}
	ack := signBody(map[string]interface{}{"pid": "1", "action": "transfer_candidates.ack.v1", "ids": fmt.Sprintf("%d,%d", rows[0].ID, rows[1].ID)})
	if rec := doPost(e, path+"/ack", ack); rec.Code == http.StatusOK {
		t.Fatal("cross-merchant acknowledgement accepted")
	}
	if pending, err := data.ListPaymentCandidates(key.ID); err != nil || len(pending) != 1 {
		t.Fatal("mixed batch partially acknowledged")
	}
	ack = signBody(map[string]interface{}{"pid": "1", "action": "transfer_candidates.ack.v1", "ids": fmt.Sprint(rows[0].ID)})
	ack["ids"] = fmt.Sprint(rows[1].ID)
	if rec := doPost(e, path+"/ack", ack); rec.Code != http.StatusUnauthorized {
		t.Fatal("tampered ack IDs accepted")
	}
	ack["ids"] = fmt.Sprint(rows[0].ID)
	for i := 0; i < 2; i++ {
		if rec := doPost(e, path+"/ack", ack); rec.Code != http.StatusOK {
			t.Fatalf("idempotent ack: %s", rec.Body.String())
		}
	}
	if pending, err := data.ListPaymentCandidates(key.ID); err != nil || len(pending) != 0 {
		t.Fatal("ack did not hide delivered candidate")
	}
	if err := dao.Mdb.First(&first, rows[0].ID).Error; err != nil || first.AcknowledgedAtMs == 0 || first.ReviewReason != "independent_check_required" {
		t.Fatal("ack mutated/deleted evidence")
	}
	var count int64
	_ = dao.Mdb.Model(&mdb.Orders{}).Where("status = ?", mdb.StatusPaySuccess).Count(&count).Error
	if count != 0 {
		t.Fatal("delivery acknowledgement became a payment")
	}
}

func TestFixedPaymentModeIsSignedAndPersistent(t *testing.T) {
	e := setupTestEnv(t)
	useRspErrorHTTPStatuses(e)
	if err := dao.Mdb.Where("network = ? AND symbol = ?", mdb.NetworkBsc, "USDT").FirstOrCreate(&mdb.ChainToken{
		Network: mdb.NetworkBsc, Symbol: "USDT", ContractAddress: "0x1111111111111111111111111111111111111111", Decimals: 18, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// Replace only test wallets with a syntactically valid synthetic EVM address.
	if err := dao.Mdb.Model(&mdb.WalletAddress{}).Where("network = ?", mdb.NetworkBsc).
		Update("address", "0x2222222222222222222222222222222222222222").Error; err != nil {
		t.Fatal(err)
	}
	if err := dao.Mdb.Create(&mdb.WalletAddress{Network: mdb.NetworkBsc, Address: "0x3333333333333333333333333333333333333333", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	body := signBody(map[string]interface{}{
		"pid": "1", "order_id": "fixed-signed", "amount": 100,
		"token": "USDT", "currency": "CNY", "network": mdb.NetworkBsc,
		"notify_url": "https://93.184.216.34/notify", "fixed_payment_intent": true,
	})
	body["fixed_payment_intent"] = false
	if rec := doPost(e, "/payments/gmpay/v1/order/create-transaction", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned mode downgrade accepted: %d %s", rec.Code, rec.Body.String())
	}
	body["fixed_payment_intent"] = true
	var tradeID interface{}
	for i := 0; i < 2; i++ {
		rec := doPost(e, "/payments/gmpay/v1/order/create-transaction", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("create/retry: %d %s", rec.Code, rec.Body.String())
		}
		payload, ok := parseResp(t, rec)["data"].(map[string]interface{})
		if !ok || payload["payment_terms"] == nil {
			t.Fatalf("missing terms: %s", rec.Body.String())
		}
		if i == 0 {
			tradeID = payload["trade_id"]
		} else if payload["trade_id"] != tradeID {
			t.Fatal("retry created another intent")
		}
	}
}
