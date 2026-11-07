//go:build windows

package beads

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// winTestWindowsDir is the host's actual Windows directory.
func winTestWindowsDir(t *testing.T) string {
	t.Helper()
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		t.Fatalf("GetSystemWindowsDirectory: %v", err)
	}
	return dir
}

// TestWindowsBoundaryRegression drives the real isPathInSafeBoundary on a
// Windows host (GH#6101): the system directories must be denied, as /etc and
// /usr are on POSIX, while ordinary paths stay admitted.
func TestWindowsBoundaryRegression(t *testing.T) {
	winDir := winTestWindowsDir(t)
	for _, path := range []string{
		winDir,
		filepath.Join(winDir, "System32"),
		filepath.Join(winDir, "System32", "not-yet-created", ".beads"),
		filepath.Join(winDir[:3], "Program Files", "beads"),
	} {
		if isPathInSafeBoundary(path) {
			t.Errorf("isPathInSafeBoundary(%q) = true, want false (Windows system directory)", path)
		}
	}
	for _, path := range []string{
		filepath.Join(t.TempDir(), ".beads"),
		filepath.Join(winDir+"-sibling", ".beads"),
	} {
		if !isPathInSafeBoundary(path) {
			t.Errorf("isPathInSafeBoundary(%q) = false, want true", path)
		}
	}
}
