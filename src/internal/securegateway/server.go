// Package securegateway is the independently built, minimal deployment profile.
// It never registers the upstream admin, password, install, public cashier,
// manual-payment, EPay/OkPay or Telegram command routes.
//
// There is no public management surface and no web admin UI. The only
// operator-facing route is GET /status: nginx forwards just the four service
// paths to this listener, so /status is reachable only through an SSH tunnel
// (ssh -L 18081:127.0.0.1:18081 RN) and never from the internet.
package securegateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/controller/comm"
	"github.com/GMWalletApp/epusdt/middleware"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	paymentrequest "github.com/GMWalletApp/epusdt/model/request"
	paymentservice "github.com/GMWalletApp/epusdt/model/service"
	responsehttp "github.com/GMWalletApp/epusdt/util/http"
	"github.com/GMWalletApp/epusdt/util/servicemac"
	"github.com/gookit/validate"
	"github.com/labstack/echo/v4"
	em "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

type Server struct {
	db  *gorm.DB
	cfg Config
}

// nonceRecord deduplicates signed service requests. It is the only table this
// profile creates; payment/evidence history is owned by the upstream schema.
type nonceRecord struct {
	Nonce string    `gorm:"primaryKey;size:64"`
	At    time.Time `gorm:"index"`
}

func New(db *gorm.DB, cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// nonceRecord is the service-request replay guard; it is the only table this
	// profile still creates on its own.
	if err := db.AutoMigrate(&nonceRecord{}); err != nil {
		return nil, err
	}
	return &Server{db: db, cfg: cfg}, nil
}

func deny() error { return echo.NewHTTPError(http.StatusForbidden, "request denied") }

func (s *Server) Handler() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		code := 500
		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
		}
		_ = c.JSON(code, map[string]string{"error": http.StatusText(code)})
	}
	e.Use(em.Recover(), em.BodyLimit("64K"))
	// Global limits use no attacker-controlled forwarding headers or unbounded maps.
	limiter := rate.NewLimiter(10, 20)
	e.Pre(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !limiter.Allow() {
				return echo.NewHTTPError(429)
			}
			h := c.Response().Header()
			h.Set("Cache-Control", "no-store")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
			if c.Request().URL.RawPath != "" || c.Request().URL.RawQuery != "" {
				return echo.NewHTTPError(404)
			}
			return next(c)
		}
	})
	// Liveness contains no database/chain details. Nginx never publishes it.
	e.GET("/healthz", func(c echo.Context) error { return c.NoContent(204) })
	// Operator status: nginx forwards nothing but the four service paths, so this
	// is reachable only through an SSH tunnel to the loopback listener.
	e.GET("/status", s.status)
	service := e.Group("/payments/gmpay/v1/order", s.serviceAuth, middleware.CheckApiSign())
	service.POST("/create-transaction", func(c echo.Context) error {
		body, ok := c.Get("request_body").(map[string]interface{})
		if !ok || body["fixed_payment_intent"] != true || body["pid"] != s.cfg.PID || body["notify_url"] != s.cfg.NotifyURL || strings.ToUpper(valueString(body["token"])) != "USDT" {
			return deny()
		}
		network := valueString(body["network"])
		if network != mdb.NetworkTron && network != mdb.NetworkBsc {
			return deny()
		}
		health, err := data.GetScanHealth(network)
		if err != nil || !scanHealthy(health, time.Now()) {
			return echo.NewHTTPError(503)
		}
		var input paymentrequest.CreateTransactionRequest
		if c.Bind(&input) != nil || !validate.Struct(input).Validate() || input.PaymentExpiresAtMs <= 0 || strings.ToUpper(input.Currency) != "CNY" || input.PaymentType != "" {
			return deny()
		}
		key, ok := c.Get(middleware.ApiKeyRowKey).(*mdb.ApiKey)
		if !ok {
			return deny()
		}
		result, err := paymentservice.CreateTransaction(&input, key)
		if err != nil {
			return (&responsehttp.Resp{}).FailJson(c, err)
		}
		result.PaymentUrl = "" // This deployment never exposes an upstream cashier.
		return (&responsehttp.Resp{}).SucJson(c, result)
	})
	service.POST("/transfer-candidates", comm.Ctrl.PaymentCandidates)
	service.POST("/transfer-candidates/ack", comm.Ctrl.AckPaymentCandidates)
	service.POST("/scan-health", func(c echo.Context) error {
		var rows []*mdb.ScanHealth
		healthy := true
		for _, network := range []string{mdb.NetworkTron, mdb.NetworkBsc} {
			row, err := data.GetScanHealth(network)
			if err != nil {
				return echo.NewHTTPError(503)
			}
			healthy = healthy && scanHealthy(row, time.Now())
			rows = append(rows, row)
		}
		return c.JSON(200, map[string]interface{}{"status_code": 200, "healthy": healthy, "data": rows})
	})
	return e
}
func valueString(v interface{}) string { s, _ := v.(string); return s }
func scanHealthy(row *mdb.ScanHealth, now time.Time) bool {
	return row != nil && row.Head > 0 && row.Scanned > 0 && row.Scanned <= row.Head && row.Head-row.Scanned <= 20 && row.HeadAtMs >= now.Add(-2*time.Minute).UnixMilli() && row.HeadAtMs <= now.Add(30*time.Second).UnixMilli() && row.PolledAtMs >= now.Add(-30*time.Second).UnixMilli()
}
func (s *Server) serviceAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		r := c.Request()
		if r.Host != s.cfg.PublicHost || r.Header.Get("Origin") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			return deny()
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		if err != nil || len(body) > 64*1024 {
			return deny()
		}
		if servicemac.Verify(r, body, s.cfg.ServiceSecret, time.Now()) != nil {
			return deny()
		}
		if s.db.Create(&nonceRecord{Nonce: r.Header.Get("X-Payment-Nonce"), At: time.Now()}).Error != nil {
			return deny()
		}
		var parsed map[string]interface{}
		if json.Unmarshal(body, &parsed) != nil || parsed["pid"] != s.cfg.PID {
			return deny()
		}
		c.Set("request_body", parsed)
		r.Body = io.NopCloser(bytes.NewReader(body))
		return next(c)
	}
}
func (s *Server) status(c echo.Context) error {
	var health []mdb.ScanHealth
	if err := s.db.Find(&health).Error; err != nil {
		return echo.NewHTTPError(503)
	}
	var orders []struct {
		TradeId            string `json:"trade_id"`
		Status             int    `json:"status"`
		Network            string `json:"network"`
		PaymentExpiresAtMs int64  `json:"expires_at_ms"`
	}
	if err := s.db.Model(&mdb.Orders{}).Select("trade_id, status, network, payment_expires_at_ms").Order("id DESC").Limit(30).Scan(&orders).Error; err != nil {
		return echo.NewHTTPError(503)
	}
	return c.JSON(200, map[string]interface{}{"scanners": health, "recent_orders": orders, "notice": "候选发现状态不代表商城已结算；到账和发货以商城独立核验为准。"})
}
