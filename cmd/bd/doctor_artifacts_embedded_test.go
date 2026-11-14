//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestEmbeddedDoctorArtifactsCleanKeepsWorkspaceAtRedirectOnlyPath pins, end
// to end, that bd doctor --check=artifacts --clean never deletes a live
// embedded Dolt workspace because of where it lives (a server-mode workspace
// there, kept by its metadata, is pinned by
// TestDoctorCleanupEntryPointsDeleteOnlyWhatTheyOwn). A workspace
// initialized at <dir>/crew/alice sits where an orchestrator expects a
// redirect-only .beads, and the cleanup removed its embedded Dolt database,
// its metadata and its config as "cruft", whether run inside it or from above
// it. Now the scan reports it for manual review, the cleanup leaves it
// byte-identical, and its issue is still there.
func TestEmbeddedDoctorArtifactsCleanKeepsWorkspaceAtRedirectOnlyPath(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	// Each run gets its own workspace, so each is independently red when the
	// cleanup deletes it.
	for _, run := range []struct {
		name  string
		above bool // run from the directory above crew/, not inside the workspace
		args  []string
	}{
		{"inside the workspace", false, []string{"doctor", "--check=artifacts", "--clean", "--yes"}},
		{"from above it, --json", true, []string{"doctor", "--check=artifacts", "--clean", "--json"}},
	} {
		t.Run(run.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "crew", "alice")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			initGitRepoAt(t, dir)
			runBDInit(t, bd, dir, "--prefix", "al")
			if out, err := bdRunWithFlockRetry(t, bd, dir, "create", "keep me", "--silent"); err != nil {
				t.Fatalf("bd create: %v\n%s", err, out)
			}
			beadsDir := filepath.Join(dir, ".beads")
			cwd := dir
			if run.above {
				cwd = parent
			}

			before := snapshotTreeForDoctorTest(t, beadsDir)
			out, err := runBDCombined(t, bd, cwd, run.args...)
			if err != nil {
				t.Fatalf("bd %s: %v\n%s", strings.Join(run.args, " "), err, out)
			}
			if strings.Contains(out, "Removed:") {
				t.Errorf("bd %s removed something:\n%s", strings.Join(run.args, " "), out)
			}
			if after := snapshotTreeForDoctorTest(t, beadsDir); !reflect.DeepEqual(after, before) {
				t.Errorf("bd %s changed %s", strings.Join(run.args, " "), beadsDir)
			}
			list, err := bdRunWithFlockRetry(t, bd, dir, "list", "--json")
			if err != nil || !strings.Contains(string(list), "keep me") {
				t.Fatalf("bd list after the cleanup = %s (err %v), want the issue still there", list, err)
			}
		})
	}
}
