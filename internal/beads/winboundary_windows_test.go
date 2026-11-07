//go:build windows

package beads

import (
	"os/user"
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

// TestWindowsBoundarySpellingRegression drives the real isPathInSafeBoundary
// with spellings that open a system directory without naming it plainly: the
// directory's $INDEX_ALLOCATION stream, and a trailing dot that Win32 strips.
func TestWindowsBoundarySpellingRegression(t *testing.T) {
	winDir := winTestWindowsDir(t)
	for _, path := range []string{
		winDir + `::$INDEX_ALLOCATION\System32`,
		winDir + `\System32::$INDEX_ALLOCATION`,
		winDir + `.\System32`,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\beads`,
	} {
		if isPathInSafeBoundary(path) {
			t.Errorf("isPathInSafeBoundary(%q) = true, want false (ambiguous spelling)", path)
		}
	}
}

// TestWindowsBoundaryProfileRegression drives the real isPathInSafeBoundary
// against the host's profile parent: another user's profile is denied, as
// /home/<other> is on POSIX, while the current user's home stays admitted.
func TestWindowsBoundaryProfileRegression(t *testing.T) {
	// The suite's TestMain points USERPROFILE at a scratch home; the policy
	// reads the account database, so the test must too.
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("no account home directory: %v", err)
	}
	home := u.HomeDir
	parent := filepath.Dir(home)
	peer := filepath.Join(parent, "some-other-nonexistent-user", ".beads")
	if isPathInSafeBoundary(peer) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (peer profile)", peer)
	}
	sibling := filepath.Join(parent, filepath.Base(home)+"2", ".beads")
	if isPathInSafeBoundary(sibling) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (peer profile sharing the home's prefix)", sibling)
	}
	own := filepath.Join(home, "src", ".beads")
	if !isPathInSafeBoundary(own) {
		t.Errorf("isPathInSafeBoundary(%q) = false, want true (current user's home)", own)
	}
}
