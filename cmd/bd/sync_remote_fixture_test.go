package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// seedSyncRemote records a sync remote in beadsDir the way bd records one, and
// proves it is readable afterwards.
//
// The tests that use this used to hand-write `sync.remote: <url>` into
// config.yaml — a key whose NAME contains a dot. That spelling is not what bd's
// writer produces and not what config.GetStringFromDir can read: it splits on
// the dot and walks nested mappings. Viper happens to find a literal dotted key
// as well as a nested one, so those fixtures worked through one reader while
// being invisible to the other — the exact asymmetry bd-zj95 was, and the
// reason the fixtures were green while the bug was live.
//
// Writing through the real writer and asserting the read-back means these tests
// can no longer encode a spelling at all. If the round trip breaks again, the
// fixture fails here, naming the property, rather than some downstream
// assertion failing for a reason that looks unrelated.
func seedSyncRemote(t *testing.T, beadsDir, remote string) {
	t.Helper()
	path := filepath.Join(beadsDir, "config.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// SetYamlConfigInDir edits an existing file; it declines to create one,
		// which is a deliberate guard against writing config into a directory
		// that is not a workspace.
		if writeErr := os.WriteFile(path, nil, 0o644); writeErr != nil { //nolint:gosec // test fixture
			t.Fatalf("create config.yaml: %v", writeErr)
		}
	} else if err != nil {
		t.Fatalf("stat config.yaml: %v", err)
	}

	if err := config.SetYamlConfigInDir(beadsDir, "sync.remote", remote); err != nil {
		t.Fatalf("record sync.remote: %v", err)
	}
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != remote {
		body, _ := os.ReadFile(path) //nolint:gosec,errcheck // diagnostic only
		t.Fatalf("sync.remote does not read back after being written: got %q, want %q\nconfig.yaml:\n%s",
			got, remote, body)
	}
}
