package securegateway

import "testing"

// The linker writes BuildCommit with -X, so the only thing worth testing here
// is that the accessor reflects whatever the linker produced and never reports
// an empty revision.
func TestBuildRevision(t *testing.T) {
	original := BuildCommit
	t.Cleanup(func() { BuildCommit = original })

	BuildCommit = ""
	if got := BuildRevision(); got != "none" {
		t.Fatalf("empty BuildCommit should read as none, got %q", got)
	}

	BuildCommit = "6283c37d004c57d065bfdb8ac283a15d78089ee3"
	if got := BuildRevision(); got != BuildCommit {
		t.Fatalf("BuildRevision should return the linked revision, got %q", got)
	}
}
