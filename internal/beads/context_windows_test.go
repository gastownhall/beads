//go:build windows

package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestIsPathInSafeBoundary_WindowsSystemDirs pins the SEC-003 boundary on
// Windows: BEADS_DIR must not resolve into the Windows system directories,
// the same way /etc and /usr are rejected on POSIX (GH#6101).
func TestIsPathInSafeBoundary_WindowsSystemDirs(t *testing.T) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	for _, path := range []string{
		filepath.Join(systemRoot, "beads"),
		filepath.Join(systemRoot, "System32", ".beads"),
		`C:\Program Files\beads`,
		`C:\Program Files (x86)\beads`,
	} {
		if isPathInSafeBoundary(path) {
			t.Errorf("isPathInSafeBoundary(%q) = true, want false (Windows system directory)", path)
		}
	}
}

// TestWindowsBoundaryContextRegression drives repository-context construction
// into a denied Windows directory through BEADS_DIR and through a workspace
// redirect, and expects the existing unsafe-location error (GH#6101). The
// denied directory is a scratch directory made a system root through the
// additive ProgramFiles(Arm) variable, with the temp carve-out moved to a
// sibling so it does not cover it.
func TestWindowsBoundaryContextRegression(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	tmp := filepath.Join(root, "tmp")
	denied := filepath.Join(sys, ".beads")
	ws := filepath.Join(root, "ws")
	for _, dir := range []string{denied, tmp, filepath.Join(ws, ".beads")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(denied, "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".beads", RedirectFileName), []byte(denied+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramFiles(Arm)", sys)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	reset := func() {
		ResetCaches()
		git.ResetCaches()
	}
	t.Cleanup(reset)

	t.Run("BEADS_DIR", func(t *testing.T) {
		t.Setenv("BEADS_DIR", denied)
		reset()
		_, err := GetRepoContext()
		if err == nil || !strings.Contains(err.Error(), "BEADS_DIR points to unsafe location") {
			t.Errorf("GetRepoContext() with BEADS_DIR=%s: err = %v, want the unsafe-location error", denied, err)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")
		t.Chdir(ws)
		reset()
		_, err := GetRepoContext()
		if err == nil || !strings.Contains(err.Error(), "BEADS_DIR points to unsafe location") {
			t.Errorf("GetRepoContext() from %s redirecting to %s: err = %v, want the unsafe-location error", ws, denied, err)
		}
	})
}
