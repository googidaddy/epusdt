package securegateway

// BuildCommit is the source revision the binary was built from. It is set at
// link time:
//
//	-ldflags "-X github.com/GMWalletApp/epusdt/internal/securegateway.BuildCommit=<sha>"
//
// Two things must stay true or the flag silently does nothing:
//
//   - it has to remain a plain package-level string variable, not a constant;
//   - something reachable from the entry point has to read it, otherwise the
//     linker drops the symbol and -X has nothing to write to. cmd/secure only
//     imports this package, so this is the one place that keeps it alive.
//
// It deliberately does not live in the config package. The previous flag
// pointed at config.BuildCommit, which never worked: cmd/secure does not
// import config, so the symbol was eliminated and every image built so far
// carried no revision at all. Importing config here to "fix" that would drag
// the MySQL, Redis and other upstream dependencies into the secure binary and
// widen its attack surface for no benefit.
var BuildCommit = "none"

// BuildRevision reports the revision the binary was built from, or "none" when
// the linker flag was not supplied.
func BuildRevision() string {
	if BuildCommit == "" {
		return "none"
	}
	return BuildCommit
}
