package securegateway

import (
	"path/filepath"
	"strings"
	"testing"
)

// testConfig returns a configuration that satisfies Validate without any
// administration surface: the profile has no admin origin, Access issuer or
// passkey allow-list to configure.
func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Listen:         "127.0.0.1:8080",
		PublicHost:     "pay.example.com",
		PID:            "test-token",
		MerchantSecret: strings.Repeat("m", 32),
		ServiceSecret:  strings.Repeat("s", 32),
		NotifyURL:      "https://shop.example.com/notify",
		TronURL:        "https://tron.example.com",
		BscHTTP:        "https://bsc.example.com/rpc",
		BscWSS:         "wss://bsc.example.com/ws",
		TronRecipient:  "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV",
		BscRecipient:   "0x2222222222222222222222222222222222222222",
		QuoteRate:      "0.14",
		DataDir:        filepath.Join(dir, "data"),
		RuntimeDir:     filepath.Join(dir, "runtime"),
		SecurityDir:    filepath.Join(dir, "security"),
	}
}
