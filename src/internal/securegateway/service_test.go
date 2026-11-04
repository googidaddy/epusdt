package securegateway

import (
	"bytes"
	"encoding/json"
	"github.com/GMWalletApp/epusdt/config"
	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/util/servicemac"
	"github.com/GMWalletApp/epusdt/util/sign"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecurePolicyCreatesFourDecimalFixedOrderThroughBothMACs(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	cfg := testConfig(t)
	cfg.NotifyURL = "https://93.184.216.34/notify"
	old := config.SettingsGetString
	defer func() { config.SettingsGetString = old }()
	s, err := New(dao.Mdb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedPolicy(cfg); err != nil {
		t.Fatal(err)
	}
	if err := data.ReloadSettings(); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	now := time.Now()
	if err := data.RecordScanHead(mdb.NetworkBsc, 100, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := data.SaveScanProgress(mdb.NetworkBsc, 100, "synthetic-hash"); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		params := map[string]interface{}{"pid": cfg.PID, "order_id": "secure-precision", "amount": 123.45, "currency": "cny", "token": "usdt", "network": mdb.NetworkBsc, "notify_url": cfg.NotifyURL, "fixed_payment_intent": true}
		params["payment_expires_at_ms"] = now.Add(time.Minute).UnixMilli()
		params["signature"], err = sign.GetHMACSHA256(params, cfg.MerchantSecret)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(params)
		r := httptest.NewRequest("POST", "https://"+cfg.PublicHost+"/payments/gmpay/v1/order/create-transaction", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		if err := servicemac.Sign(r, b, cfg.ServiceSecret); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var result struct {
			Data struct {
				Terms *mdb.PaymentTerms `json:"payment_terms"`
				URL   string            `json:"payment_url"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Data.Terms == nil {
			t.Fatalf("secure create rejected %d %s", w.Code, w.Body.String())
		}
		if result.Data.Terms.RawAmount != "17283000000000000000" || result.Data.Terms.Rate != "0.14" || result.Data.Terms.ExpiresAtMs != now.Add(time.Minute).UnixMilli() || result.Data.URL != "" {
			t.Fatalf("unsafe quote/cashier result: %+v", result)
		}
	}
	var count int64
	dao.Mdb.Model(&mdb.Orders{}).Count(&count)
	if count != 1 {
		t.Fatal("fresh MAC retry duplicated fixed order")
	}
}

func TestServiceEnvelopeReplayBodyAndAuthority(t *testing.T) {
	defer testutil.SetupTestDatabases(t)()
	cfg := testConfig(t)
	s, err := New(dao.Mdb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	params := map[string]interface{}{"pid": "test-token", "action": "transfer_candidates.list.v1"}
	signature, err := sign.GetHMACSHA256(params, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	params["signature"] = signature
	b, _ := json.Marshal(params)
	req := httptest.NewRequest("POST", "https://pay.example.com/payments/gmpay/v1/order/transfer-candidates", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if servicemac.Sign(req, b, cfg.ServiceSecret) != nil {
		t.Fatal("sign")
	}
	send := func(r *http.Request, body []byte) int {
		copy := r.Clone(r.Context())
		copy.Body = io.NopCloser(bytes.NewReader(body))
		copy.ContentLength = int64(len(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, copy)
		return w.Code
	}
	if code := send(req, append(append([]byte{}, b...), byte(' '))); code != 403 {
		t.Fatal("raw body mutation accepted", code)
	}
	bad := req.Clone(req.Context())
	bad.Host = "other.example.com"
	if code := send(bad, b); code != 403 {
		t.Fatal("authority substitution accepted", code)
	}
	if code := send(req, b); code != 200 {
		t.Fatal("valid envelope rejected", code)
	}
	if code := send(req, b); code != 403 {
		t.Fatal("nonce replay accepted", code)
	}
	// A fresh retry keeps the business request identical, with new envelope nonce.
	if servicemac.Sign(req, b, cfg.ServiceSecret) != nil {
		t.Fatal("sign")
	}
	if code := send(req, b); code != 200 {
		t.Fatal("idempotent retry rejected", code)
	}
	req.Header.Set("X-Payment-Time", "1")
	req.Header.Set("X-Payment-MAC", servicemac.Digest(req, b, cfg.ServiceSecret))
	if code := send(req, b); code != 403 {
		t.Fatal("stale signed request accepted", code)
	}
	var count int64
	dao.Mdb.Model(&mdb.Orders{}).Count(&count)
	if count != 0 {
		t.Fatal("list created order")
	}
}
func TestScanLivenessNeedsFreshHeadAndCommittedProgress(t *testing.T) {
	now := time.Now()
	base := mdb.ScanHealth{Head: 100, Scanned: 100, HeadAtMs: now.UnixMilli(), PolledAtMs: now.UnixMilli()}
	if !scanHealthy(&base, now) {
		t.Fatal("fresh scanner not healthy")
	}
	for _, change := range []func(*mdb.ScanHealth){func(s *mdb.ScanHealth) { s.HeadAtMs = now.Add(-3 * time.Minute).UnixMilli() }, func(s *mdb.ScanHealth) { s.PolledAtMs = now.Add(-time.Minute).UnixMilli() }, func(s *mdb.ScanHealth) { s.Scanned = 1 }, func(s *mdb.ScanHealth) { s.Scanned = 101 }} {
		row := base
		change(&row)
		if scanHealthy(&row, now) {
			t.Fatal("stalled or inconsistent scanner healthy")
		}
	}
}
func TestMACRejectsWrongDirectionMethodPath(t *testing.T) {
	body := []byte(`{}`)
	secret := strings.Repeat("s", 32)
	r := httptest.NewRequest("POST", "https://pay.example.com/original", bytes.NewReader(body))
	_ = servicemac.Sign(r, body, secret)
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Method = "GET" }, func(r *http.Request) { r.URL.Path = "/other" }, func(r *http.Request) { r.Header.Set("X-Payment-Version", "gateway->merchant") }} {
		copy := r.Clone(r.Context())
		change(copy)
		if servicemac.Verify(copy, body, secret, time.Now()) == nil {
			t.Fatal("envelope context substitution")
		}
	}
}
