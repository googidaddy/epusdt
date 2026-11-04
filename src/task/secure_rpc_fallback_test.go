package task

import "testing"

// A reviewed fallback must be usable by the dialer without widening the
// allowlist: only the values handed to ConfigureSecureRPC may be dialled.
func TestSecureRPCAllowsOnlyConfiguredEndpoints(t *testing.T) {
	oldEndpoints, oldHeader, oldHTTP := secureEndpoints, secureBSCHeaderEndpoint, httpClient
	t.Cleanup(func() {
		secureEndpoints, secureBSCHeaderEndpoint, httpClient = oldEndpoints, oldHeader, oldHTTP
	})

	if err := ConfigureSecureRPC(
		[]string{"https://tron-a.example.com", "https://tron-b.example.com/key"},
		[]string{"https://bsc-a.example.com/key", "https://bsc-b.example.com/key"},
		"wss://bsc-a.example.com/ws", ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://bsc-a.example.com/key",
		"https://bsc-b.example.com/key",
		"wss://bsc-a.example.com/ws",
	} {
		if !secureEndpoints[want] {
			t.Fatalf("%s missing from allowlist", want)
		}
	}
	for _, denied := range []string{
		"https://unreviewed.example.com/key",
		"https://tron-a.example.com",
	} {
		if secureEndpoints[denied] {
			t.Fatalf("%s unexpectedly allowlisted", denied)
		}
	}
}

// Empty endpoint lists must fail closed instead of configuring a half-built
// allowlist that the scanners would then treat as usable.
func TestSecureRPCRejectsEmptyLists(t *testing.T) {
	oldEndpoints, oldHeader, oldHTTP := secureEndpoints, secureBSCHeaderEndpoint, httpClient
	t.Cleanup(func() {
		secureEndpoints, secureBSCHeaderEndpoint, httpClient = oldEndpoints, oldHeader, oldHTTP
	})
	secureEndpoints, secureBSCHeaderEndpoint = nil, ""

	if err := ConfigureSecureRPC(nil, []string{"https://bsc-a.example.com"}, "wss://bsc-a.example.com/ws", ""); err == nil {
		t.Fatal("empty TRON list accepted")
	}
	if err := ConfigureSecureRPC([]string{"https://tron-a.example.com"}, nil, "wss://bsc-a.example.com/ws", ""); err == nil {
		t.Fatal("empty BSC list accepted")
	}
	if secureEndpoints != nil || secureBSCHeaderEndpoint != "" {
		t.Fatal("failed configuration left partial state behind")
	}
}
