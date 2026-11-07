package beads

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// TestIsPathInSafeBoundaryAdmitsVarLibServiceHome is the bd-77hbfy / GH#6323
// repro: a non-root service account whose home is a specific directory below
// /var/lib (e.g. /var/lib/flow-estate) must be able to point BEADS_DIR at a
// .beads inside its own home. The account home is read from the OS account
// database, which a test cannot repoint, so the repro command runs the test
// binary under testdata/fakehome (an LD_PRELOAD getpwuid_r shim) with
// BD_REPRO_FAKE_HOME naming the service home.
func TestIsPathInSafeBoundaryAdmitsVarLibServiceHome(t *testing.T) {
	want := os.Getenv("BD_REPRO_FAKE_HOME")
	if want == "" {
		t.Skip("BD_REPRO_FAKE_HOME unset: run under the testdata/fakehome LD_PRELOAD shim")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	if u.Uid == "0" {
		t.Skip("UID 0 is never admitted")
	}
	if u.HomeDir != want {
		t.Fatalf("account home = %q, want %q (shim not loaded)", u.HomeDir, want)
	}

	own := filepath.Join(want, "releases", "r1", ".beads")
	if !isPathInSafeBoundary(own) {
		t.Errorf("isPathInSafeBoundary(%q) = false for a path in the current non-root account's /var/lib home %q, want true", own, want)
	}
	// The exception stays narrow: a sibling service's home is still rejected.
	if sibling := "/var/lib/some-other-service/.beads"; isPathInSafeBoundary(sibling) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (not the account home)", sibling)
	}
}
