//go:build windows

package beads

import (
	"os"
	"path/filepath"
	"testing"
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
