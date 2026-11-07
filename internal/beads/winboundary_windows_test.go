//go:build windows

package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveWindowsFinalPath covers the physical-resolution adapter the
// Windows boundary relies on (CreateFile + GetFinalPathNameByHandle): it
// follows directory links to where they land, appends missing tails to the
// resolved longest existing ancestor, and leaves dangling links and
// non-directory ancestors unresolved.
func TestResolveWindowsFinalPath(t *testing.T) {
	canon := func(p string) string {
		t.Helper()
		c, ok := winCleanPath(p)
		if !ok {
			t.Fatalf("winCleanPath(%q) invalid", p)
		}
		return c
	}

	t.Run("plain directory resolves to itself", func(t *testing.T) {
		dir := t.TempDir()
		got, ok := resolveWindowsFinalPath(dir)
		if !ok {
			t.Fatalf("resolveWindowsFinalPath(%q) unresolved", dir)
		}
		if !strings.EqualFold(canon(got), canon(dir)) {
			t.Errorf("resolveWindowsFinalPath(%q) = %q, want %q", dir, got, dir)
		}
	})

	t.Run("link resolves to its target", func(t *testing.T) {
		if !winLinkResolutionAvailable(t) {
			t.Skip("directory links cannot be resolved on this kernel (wine?): needs native Windows")
		}
		base := t.TempDir()
		target := filepath.Join(base, "target")
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		link := filepath.Join(base, "link")
		if !makeDirLink(t, link, target) {
			t.Skip("cannot create a directory link for the fixture")
		}
		t.Cleanup(func() { _ = os.Remove(link) })
		got, ok := resolveWindowsFinalPath(link)
		if !ok {
			t.Fatalf("resolveWindowsFinalPath(%q) unresolved", link)
		}
		if !strings.EqualFold(canon(got), canon(target)) {
			t.Errorf("resolveWindowsFinalPath(%q) = %q, want target %q", link, got, target)
		}
	})

	t.Run("missing tail appended to resolved ancestor", func(t *testing.T) {
		base := t.TempDir()
		got, ok := resolveWindowsFinalPath(filepath.Join(base, "a", "b", ".beads"))
		if !ok {
			t.Fatalf("resolveWindowsFinalPath(missing tail) unresolved")
		}
		baseFinal, ok := resolveWindowsFinalPath(base)
		if !ok {
			t.Fatalf("resolveWindowsFinalPath(%q) unresolved", base)
		}
		want := canon(baseFinal) + `\a\b\.beads`
		if !strings.EqualFold(canon(got), want) {
			t.Errorf("resolveWindowsFinalPath(missing tail) = %q, want %q", got, want)
		}
	})

	t.Run("non-directory ancestor unresolved", func(t *testing.T) {
		base := t.TempDir()
		file := filepath.Join(base, "file.txt")
		if err := os.WriteFile(file, []byte{}, 0o640); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if _, ok := resolveWindowsFinalPath(filepath.Join(file, "sub")); ok {
			t.Error("resolveWindowsFinalPath(path under a file) resolved, want unresolved")
		}
	})

	t.Run("dangling link unresolved", func(t *testing.T) {
		// Telling a dangling link from an absent entry needs a no-follow
		// probe; wine follows its symlinks everywhere, so this pins native
		// behavior only.
		if !winLinkResolutionAvailable(t) {
			t.Skip("directory links cannot be resolved on this kernel (wine?): needs native Windows")
		}
		base := t.TempDir()
		dangling := filepath.Join(base, "dangling")
		if !makeDirLink(t, dangling, filepath.Join(base, "missing-target")) {
			t.Skip("cannot create a directory link for the fixture")
		}
		t.Cleanup(func() { _ = os.Remove(dangling) })
		if _, ok := resolveWindowsFinalPath(dangling); ok {
			t.Errorf("resolveWindowsFinalPath(dangling %q) resolved, want unresolved", dangling)
		}
	})

	t.Run("short name resolves to long form", func(t *testing.T) {
		const short = `C:\PROGRA~1`
		if _, err := os.Stat(short); err != nil {
			t.Skipf("no 8.3 short name at %s: %v", short, err)
		}
		got, ok := resolveWindowsFinalPath(short)
		if !ok {
			t.Fatalf("resolveWindowsFinalPath(%q) unresolved", short)
		}
		if !strings.EqualFold(canon(got), `C:\Program Files`) {
			t.Errorf("resolveWindowsFinalPath(%q) = %q, want C:\\Program Files", short, got)
		}
	})
}

// TestWindowsProfilesDirectory pins the userenv.dll wrapper's buffer
// handling: when the API answers, the result must be a usable directory
// path (never a volume root), and a lookup failure must report unavailable
// rather than garbage.
func TestWindowsProfilesDirectory(t *testing.T) {
	got, ok := windowsProfilesDirectory()
	if !ok {
		t.Skip("GetProfilesDirectoryW unavailable on this host")
	}
	canon, valid := winCleanPath(got)
	if !valid {
		t.Fatalf("profiles directory %q is not a valid path", got)
	}
	if winIsVolumeRoot(canon) {
		t.Errorf("profiles directory %q is a volume root", got)
	}
	if parent, base := splitWinPath(canon); base == "" || parent == canon {
		t.Errorf("profiles directory %q has no parent component", got)
	}
}
