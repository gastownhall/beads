package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"path/filepath"
)

// TestImportFromLocalJSONLFullQuarantinesInvalidRows pins the explicit
// recovery paths (`bd bootstrap`, `bd init --from-jsonl`) through their
// shared helper (GH#4492 AC-4): valid rows and memories import, invalid rows
// are quarantined beside the source, and the import reports the skips
// instead of failing.
func TestImportFromLocalJSONLFullQuarantinesInvalidRows(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines)

	store := &captureOptsStore{prefix: "ce"}
	result, err := importFromLocalJSONLFull(context.Background(), store, source)
	if err != nil {
		t.Fatalf("importFromLocalJSONLFull: %v", err)
	}
	if result.Issues != 1 {
		t.Errorf("imported %d issues, want 1 (ce-1); invalid = %d", result.Issues, result.Invalid)
	}
	if result.Invalid != 1 {
		t.Errorf("result.Invalid = %d, want 1", result.Invalid)
	}
	if store.gotOpts.ConflictSkip {
		t.Error("explicit recovery path must keep UPSERT (ConflictSkip=false)")
	}
	saved, err := os.ReadFile(filepath.Join(dir, "issues.rejected.jsonl"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("quarantine = %q, want ce-2's exact bytes", saved)
	}
}

// TestSyncImportArgs pins the legacy git-hook sync importer's argv
// (GH#4492 AC-4): quiet, skip-invalid, and the default quarantine beside the
// source file — the hook must never run a strict import that one stale row
// could wedge the pull with.
func TestSyncImportArgs(t *testing.T) {
	got := syncImportArgs(filepath.Join("w", "issues.jsonl"))
	want := []string{
		"import", "--quiet", "--skip-invalid",
		"--rejects", filepath.Join("w", "issues.rejected.jsonl"),
		filepath.Join("w", "issues.jsonl"),
	}
	if len(got) != len(want) {
		t.Fatalf("syncImportArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("syncImportArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestForwardImportSyncWarnings pins the hook's warning forwarding
// (GH#4492 AC-4): every "warning:"-prefixed line the subprocess printed is
// relayed (so an operator hears about skipped records on a SUCCESSFUL
// import too), and nothing else — normal quiet output stays suppressed.
func TestForwardImportSyncWarnings(t *testing.T) {
	stderr := captureStderr(t, func() {
		forwardImportSyncWarnings("post-merge", []byte(strings.Join([]string{
			"warning: import skipped 1 invalid record(s) from issues.jsonl; preserved in issues.rejected.jsonl",
			"imported 1 issues from issues.jsonl",
			"warning:   line 2 (id ce-2): invalid status: verify",
			"",
		}, "\n")))
	})
	for _, want := range []string{
		"beads: post-merge import: import skipped 1 invalid record(s)",
		"beads: post-merge import:   line 2 (id ce-2): invalid status: verify",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("forwarded warnings = %q, want it to contain %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "imported 1 issues") {
		t.Errorf("forwarded warnings = %q, want normal output suppressed", stderr)
	}
}
