//go:build cgo

package main

import (
	"path/filepath"
	"testing"
)

// TestShowMissingIDPreservesLastTouched covers a failed show lookup at the
// process boundary. A missing ID in text or JSON mode must leave an existing
// last-touched marker byte-for-byte unchanged, including mtime, rather than
// replacing it with the unknown raw argument.
func TestShowMissingIDPreservesLastTouched(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	id := runBDStdout(t, binPath, workDir, "q", "missing-id last-touched fixture")
	if id == "" {
		t.Fatal("bd q returned empty issue ID")
	}
	const missingID = "bd-does-not-exist"
	lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)

	for _, mode := range []struct {
		name string
		args []string
	}{
		{name: "text", args: []string{"show", missingID}},
		{name: "json", args: []string{"show", missingID, "--json"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			sentinel := []byte(id + "\n")
			mtime := setLastTouchedSentinel(t, lastTouchedPath, sentinel)

			result := runLastTouchedProcess(t, binPath, workDir, mode.args...)
			if result.exitCode == 0 {
				t.Fatalf("show of missing ID exit code = 0, want non-zero\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
			}
			assertLastTouchedUnchanged(t, lastTouchedPath, sentinel, mtime)
		})
	}
}
