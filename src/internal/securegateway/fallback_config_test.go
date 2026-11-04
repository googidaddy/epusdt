package securegateway

import "testing"

// Reviewed failover endpoints are optional, but when present they must satisfy
// exactly the same rules as the primary endpoint: the SSRF allowlist may only
// grow by values an operator wrote down and the validator accepted.
func TestFallbackEndpointsValidation(t *testing.T) {
	base := func() Config { return testConfig(t) }

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"no fallbacks", func(c *Config) {}, false},
		{"tron fallback without path", func(c *Config) {
			c.TronFallbacks = []string{"https://tron-b.example.com"}
		}, false},
		{"tron fallback with credential path", func(c *Config) {
			c.TronFallbacks = []string{"https://tron-b.example.com/8af2e00b2ddb7c42cf27e27853697794"}
		}, false},
		{"tron fallback trailing slash", func(c *Config) {
			c.TronFallbacks = []string{"https://tron-b.example.com/"}
		}, true},
		{"tron fallback plain http", func(c *Config) {
			c.TronFallbacks = []string{"http://tron-b.example.com"}
		}, true},
		{"tron fallback duplicates primary", func(c *Config) {
			c.TronFallbacks = []string{c.TronURL}
		}, true},
		{"tron fallback empty string", func(c *Config) {
			c.TronFallbacks = []string{"   "}
		}, true},
		{"bsc fallback valid", func(c *Config) {
			c.BscFallbacks = []string{"https://bsc-b.example.com/v2/key"}
		}, false},
		{"bsc fallback plain http", func(c *Config) {
			c.BscFallbacks = []string{"http://bsc-b.example.com/v2/key"}
		}, true},
		{"bsc fallback duplicates primary", func(c *Config) {
			c.BscFallbacks = []string{c.BscHTTP}
		}, true},
		{"bsc fallback duplicates wss", func(c *Config) {
			c.BscFallbacks = []string{c.BscWSS}
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation failure, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected valid config, got %v", err)
			}
		})
	}
}

// The primary endpoint must stay first so it keeps winning node selection while
// healthy; fallbacks only become candidates after a failure is recorded.
func TestEndpointListsKeepPrimaryFirst(t *testing.T) {
	c := testConfig(t)
	c.TronFallbacks = []string{"https://tron-b.example.com"}
	c.BscFallbacks = []string{"https://bsc-b.example.com/v2/key"}

	tron := c.TronURLs()
	if len(tron) != 2 || tron[0] != c.TronURL || tron[1] != "https://tron-b.example.com" {
		t.Fatalf("tron order = %v", tron)
	}
	bsc := c.BscHTTPSURLs()
	if len(bsc) != 2 || bsc[0] != c.BscHTTP || bsc[1] != "https://bsc-b.example.com/v2/key" {
		t.Fatalf("bsc order = %v", bsc)
	}
}
