package securegateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/GMWalletApp/epusdt/crypto"
	"github.com/GMWalletApp/epusdt/util/securehttp"
	"github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"
)

type Config struct {
	Listen         string `json:"listen"`
	PublicHost     string `json:"public_host"`
	PID            string `json:"pid"`
	MerchantSecret string `json:"merchant_secret"`
	ServiceSecret  string `json:"service_secret"`
	NotifyURL      string `json:"notify_url"`
	TronURL        string `json:"tron_url"`
	TronAPIKey     string `json:"tron_api_key"`
	BscHTTP        string `json:"bsc_https"`
	BscHeaderHTTP  string `json:"bsc_header_https,omitempty"`
	BscWSS         string `json:"bsc_wss"`
	TronRecipient  string `json:"tron_recipient"`
	BscRecipient   string `json:"bsc_recipient"`
	QuoteRate      string `json:"quote_rate_usdt_per_cny"`
	DataDir        string `json:"data_dir"`
	RuntimeDir     string `json:"runtime_dir"`
	SecurityDir    string `json:"security_dir"`
	// Optional failover endpoints. The primary endpoints above stay authoritative;
	// these are only used after the gateway marks a node as failing. Every entry
	// must pass the same securehttp.Endpoint check, so the SSRF allowlist only
	// grows by explicitly reviewed operator values.
	TronFallbacks []string `json:"tron_url_fallbacks,omitempty"`
	BscFallbacks  []string `json:"bsc_https_fallbacks,omitempty"`
}

func ReadConfig(path string) (Config, error) {
	var c Config
	stat, err := os.Stat(path)
	if err != nil {
		return c, errors.New("private configuration unavailable")
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return c, errors.New("configuration must be a private regular file (0600)")
	}
	file, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return c, errors.New("invalid private configuration")
	}
	var extra interface{}
	if decoder.Decode(&extra) != io.EOF {
		return c, errors.New("trailing configuration content")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	invalid := errors.New("secure gateway configuration incomplete or invalid")
	if c.Listen != "0.0.0.0:8080" && c.Listen != "127.0.0.1:8080" {
		return invalid
	}
	if strings.ContainsAny(c.PublicHost, "/:?#@") || !strings.Contains(c.PublicHost, ".") {
		return invalid
	}
	if c.PID == "" || len(c.PID) > 64 || len(c.MerchantSecret) < 32 || len(c.ServiceSecret) < 32 || c.MerchantSecret == c.ServiceSecret {
		return invalid
	}
	for _, value := range []string{c.MerchantSecret, c.ServiceSecret} {
		if strings.Contains(value, "REPLACE") {
			return invalid
		}
	}
	for _, endpoint := range []string{c.NotifyURL, c.TronURL, c.BscHTTP} {
		if _, err := securehttp.Endpoint(endpoint); err != nil {
			return invalid
		}
	}
	if c.BscHeaderHTTP != "" {
		if _, err := securehttp.Endpoint(c.BscHeaderHTTP); err != nil {
			return invalid
		}
	}
	tron, _ := url.Parse(c.TronURL)
	if tron.Path != "" {
		return invalid
	}
	bsc, err := url.Parse(c.BscWSS)
	if err != nil || bsc.Scheme != "wss" {
		return invalid
	}
	bsc.Scheme = "https"
	if _, err := securehttp.Endpoint(bsc.String()); err != nil {
		return invalid
	}
	recipient, err := crypto.DecodeCheck(c.TronRecipient)
	if err != nil || len(recipient) != 21 || recipient[0] != 0x41 {
		return invalid
	}
	if !common.IsHexAddress(c.BscRecipient) || common.HexToAddress(c.BscRecipient) == (common.Address{}) {
		return invalid
	}
	rate, err := decimal.NewFromString(c.QuoteRate)
	if err != nil || rate.Sign() <= 0 || rate.Exponent() < -12 || rate.GreaterThan(decimal.NewFromInt(1000)) {
		return invalid
	}
	seen := map[string]bool{}
	for _, dir := range []string{c.DataDir, c.RuntimeDir, c.SecurityDir} {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" || seen[dir] {
			return invalid
		}
		seen[dir] = true
	}
	// Failover endpoints are held to the same transport rules as the primary ones
	// (public HTTPS, no query/fragment). A path is allowed here because providers
	// such as Chainstack embed the credential in the path; a trailing slash is not,
	// because the scanner appends "/walletsolidity/..." to the base.
	for _, raw := range c.TronFallbacks {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == c.TronURL || strings.HasSuffix(trimmed, "/") {
			return invalid
		}
		if _, err := securehttp.Endpoint(trimmed); err != nil {
			return invalid
		}
	}
	for _, raw := range c.BscFallbacks {
		if strings.TrimSpace(raw) == "" || raw == c.BscHTTP || raw == c.BscHeaderHTTP || raw == c.BscWSS {
			return invalid
		}
		if _, err := securehttp.Endpoint(raw); err != nil {
			return invalid
		}
	}
	return nil
}

// TronURLs returns the primary TRON endpoint followed by any reviewed fallbacks.
func (c Config) TronURLs() []string {
	out := make([]string, 0, len(c.TronFallbacks)+1)
	out = append(out, c.TronURL)
	for _, raw := range c.TronFallbacks {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// BscHTTPSURLs returns the primary BSC HTTP endpoint followed by any reviewed fallbacks.
func (c Config) BscHTTPSURLs() []string {
	out := make([]string, 0, len(c.BscFallbacks)+1)
	out = append(out, c.BscHTTP)
	for _, raw := range c.BscFallbacks {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
