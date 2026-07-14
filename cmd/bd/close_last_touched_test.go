//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runBDStdout runs the bd binary with args in workDir and returns trimmed
// stdout. stderr is captured separately so auto-pull/warning lines don't
// pollute parsed values (e.g. an issue ID from `bd q`).
func runBDStdout(t *testing.T, binPath, workDir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd %v in %s: %v\nstdout: %s\nstderr: %s", args, workDir, err, outBuf.String(), errBuf.String())
	}
	return strings.TrimSpace(outBuf.String())
}

// TestClose_UpdatesLastTouched verifies `bd close <id>` records the closed issue
// as the last-touched issue, honoring its documented contract (GH#3965). Before
// the fix, close only set last-touched on the --claim-next path, so a plain
// close left the marker pointing at a stale issue.
func TestClose_UpdatesLastTouched(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	// Create an issue to close.
	id := runBDStdout(t, binPath, workDir, "q", "Issue to close")
	if id == "" {
		t.Fatal("bd q returned empty issue ID")
	}

	// Point last-touched at a sentinel so the assertion proves close updated it
	// (independent of whatever `bd q` may have written).
	lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)
	if err := os.WriteFile(lastTouchedPath, []byte("SENTINEL-NOT-CLOSED\n"), 0600); err != nil {
		t.Fatalf("seed last-touched sentinel: %v", err)
	}

	// Close the issue.
	runBDStdout(t, binPath, workDir, "close", id, "--reason", "done via test")

	data, err := os.ReadFile(lastTouchedPath)
	if err != nil {
		t.Fatalf("read last-touched after close: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != id {
		t.Errorf("last-touched after close = %q, want closed id %q", got, id)
	}
}
