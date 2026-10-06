package bdhttp_test

// Written fresh for OSS beads S6: pins the bee-ghosttrack CHANGES_REQUESTED
// finding on #7288, should-fix 1 — `bd connect --clear` restoring a
// workspace's previous backend selection needs Attach to have recorded what
// that previous backend WAS, onto the sidecar, at connect time.

import (
	"net/url"
	"testing"

	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpclient"
)

func mustAttachTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

// TestAttachRecordsThePriorBackendAsPreviousBackend is the first-connect
// case: a workspace that selected "dolt" before ever running `bd connect`
// must have Attach record "dolt" as PreviousBackend, so --clear can restore
// it later.
func TestAttachRecordsThePriorBackendAsPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: "dolt", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend = %q, want %q", got.PreviousBackend, "dolt")
	}

	after, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("reload metadata.json: %v", err)
	}
	// Asserts on the raw stored field, not GetBackend(): GetBackend() falls
	// back to BackendDolt for any name backendnames.Has doesn't recognize
	// (TestGetBackendAllowlist in internal/configfile), and "http" is only
	// ever added there by bdhttp.Register/httpclient.Register — a
	// once-per-process, panics-on-duplicate production wiring call this test
	// package must not make. The raw field is exactly what Attach controls.
	if after.Backend != httpclient.Backend {
		t.Errorf("metadata.json backend = %q after Attach, want %q", after.Backend, httpclient.Backend)
	}
}

// TestAttachOnAFreshWorkspaceRecordsTheImplicitDoltDefault covers a
// workspace with no metadata.json at all: GetBackend()'s own default
// ("dolt") is what a future --clear restores to, matching what every other
// backend-selection read in this codebase already treats as this
// workspace's backend.
func TestAttachOnAFreshWorkspaceRecordsTheImplicitDoltDefault(t *testing.T) {
	beadsDir := t.TempDir()
	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend = %q, want %q (configfile.BackendDolt)", got.PreviousBackend, "dolt")
	}
}

// TestAttachOnAReconnectKeepsTheEarlierPreviousBackend covers a workspace
// ALREADY on http (a bare re-connect, or --force re-pinning the same
// backend): Attach must not overwrite an already-recorded PreviousBackend
// with "http" itself, or a chain of reconnects would forget what --clear is
// supposed to restore.
func TestAttachOnAReconnectKeepsTheEarlierPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: "dolt", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	first := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, first); err != nil {
		t.Fatalf("first Attach: %v", err)
	}

	// A second connect to a different url, same workspace, same backend
	// ("http", already selected) — the shape `bd connect --force` to a new
	// server produces for an already-http workspace.
	second := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:9090")}
	if err := bdhttp.Attach(beadsDir, second); err != nil {
		t.Fatalf("second Attach: %v", err)
	}

	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend after a reconnect = %q, want the original %q carried forward, not the just-left \"http\"", got.PreviousBackend, "dolt")
	}
	if got.BaseURL.String() != "http://127.0.0.1:9090" {
		t.Errorf("BaseURL = %q, want the second connect's url", got.BaseURL.String())
	}
}

// TestAttachHonorsACallerSuppliedPreviousBackend covers Connect's doc
// contract: a caller that already set target.PreviousBackend itself (none
// of this package's own callers do today) must have that value win over
// Attach's own inference.
func TestAttachHonorsACallerSuppliedPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: "dolt", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080"), PreviousBackend: "mysql"}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "mysql" {
		t.Errorf("PreviousBackend = %q, want the caller-supplied %q to win over the inferred %q", got.PreviousBackend, "mysql", "dolt")
	}
}
