package securegateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/config"
	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/util/servicemac"
)

// Optional two-repository wire acceptance. The test-only loopback relay supplies
// the outer MAC; the merchant's actual serialized body and business HMAC pass
// unchanged into the production secure handler. Production TLS/egress are tested
// separately and are never weakened for this harness.
func TestCrossRepositoryGatewayWire(t *testing.T) {
	dir := os.Getenv("PAYMENT_WIRE_LAB_DIR")
	if dir == "" {
		t.Skip("wire lab not selected")
	}
	defer testutil.SetupTestDatabases(t)()
	cfg := testConfig(t)
	cfg.NotifyURL = "https://93.184.216.34/notify"
	s, err := New(dao.Mdb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	previous := config.SettingsGetString
	defer func() { config.SettingsGetString = previous }()
	if err := SeedPolicy(cfg); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{mdb.NetworkTron, mdb.NetworkBsc} {
		if err := data.RecordScanHead(network, 100, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if err := data.SaveScanProgress(network, 100, "synthetic-hash"); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if err != nil || len(body) > 65536 {
			w.WriteHeader(400)
			return
		}
		r.Host = cfg.PublicHost
		r.URL.Host = cfg.PublicHost
		r.URL.Scheme = "https"
		r.Body = io.NopCloser(bytes.NewReader(body))
		if servicemac.Sign(r, body, cfg.ServiceSecret) != nil {
			w.WriteHeader(500)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer relay.Close()
	if err := os.WriteFile(filepath.Join(dir, "endpoint"), []byte(relay.URL), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("merchant wire client did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var count int64
	if dao.Mdb.Model(&mdb.Orders{}).Count(&count).Error != nil || count != 2 {
		t.Fatalf("wanted two idempotent network orders, got %d", count)
	}
}
