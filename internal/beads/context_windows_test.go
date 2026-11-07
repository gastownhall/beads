//go:build windows

package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// makeDirLink makes link point at the directory target, preferring an NTFS
// junction (needs no privilege on real Windows) and falling back to a symlink
// (wine maps it onto a unix symlink; on real Windows it needs developer
// mode). It reports false when neither form can be created so callers can
// skip rather than silently test nothing.
func makeDirLink(t *testing.T, link, target string) bool {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err == nil {
		return true
	} else {
		t.Logf("mklink /J %s failed: %v: %s", link, err, strings.TrimSpace(string(out)))
	}
	if err := os.Symlink(target, link); err == nil {
		return true
	} else {
		t.Logf("symlink %s -> %s failed: %v", link, target, err)
	}
	return false
}

// winLinkResolutionAvailable reports whether this kernel resolves directory
// links in final paths. Real Windows does; wine's
// GetFinalPathNameByHandle echoes the spelling used to open instead of the
// target, and its no-follow probes follow anyway, so link-dependent
// assertions cannot run there. They still pin behavior on native Windows.
func winLinkResolutionAvailable(t *testing.T) bool {
	t.Helper()
	base := t.TempDir()
	target := filepath.Join(base, "probe-target")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Skipf("mkdir probe target: %v", err)
	}
	link := filepath.Join(base, "probe-link")
	if !makeDirLink(t, link, target) {
		return false
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	got, ok := resolveWindowsFinalPath(link)
	if !ok {
		return false
	}
	canonGot, ok1 := winCleanPath(got)
	canonTarget, ok2 := winCleanPath(target)
	return ok1 && ok2 && strings.EqualFold(canonGot, canonTarget)
}

// TestWindowsBoundaryRegression proves the live SEC-003 boundary on Windows
// through isPathInSafeBoundary: the machine's actual Windows directory is
// denied, and so is an alias (junction or symlink) planted in the temp dir
// that points into it — including not-yet-created descendants reached
// through the alias. Fixtures live in the temp dir; system directories are
// never written.
func TestWindowsBoundaryRegression(t *testing.T) {
	sysDir := os.Getenv("SystemRoot")
	if sysDir == "" {
		sysDir = `C:\Windows`
	}
	if info, err := os.Stat(sysDir); err != nil || !info.IsDir() {
		t.Skipf("no Windows directory at %s: %v", sysDir, err)
	}
	if isPathInSafeBoundary(filepath.Join(sysDir, "beads")) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (system directory)", filepath.Join(sysDir, "beads"))
	}

	base := t.TempDir()
	if !winLinkResolutionAvailable(t) {
		t.Skip("directory links cannot be resolved on this kernel (wine?): alias assertions need native Windows")
	}
	link := filepath.Join(base, "sysalias")
	if !makeDirLink(t, link, sysDir) {
		t.Skip("cannot create a directory link for the alias fixture")
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	if isPathInSafeBoundary(link) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (alias into %s)", link, sysDir)
	}
	desc := filepath.Join(link, "System32", ".beads")
	if isPathInSafeBoundary(desc) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (missing descendant through alias)", desc)
	}

	// Control: the temp fixture root itself stays admitted.
	if !isPathInSafeBoundary(base) {
		t.Errorf("isPathInSafeBoundary(%q) = false, want true (temp fixture root)", base)
	}
}
