//go:build windows

package beads

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
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

	// An 8.3 short name is an alias the OS resolves on its own.
	t.Run("short name", func(t *testing.T) {
		long := filepath.Join(winDir[:3], "Program Files")
		short := winTestShortName(t, long)
		for _, path := range []string{
			short,
			filepath.Join(short, "not-yet-created", ".beads"),
		} {
			if isPathInSafeBoundary(path) {
				t.Errorf("isPathInSafeBoundary(%q) = true, want false (short name of %s)", path, long)
			}
		}
	})

	// An alias into the Windows directory is denied by its physical
	// location, and so is a not-yet-created descendant reached through it.
	aliases := map[string]func(t *testing.T, link, target string){
		"junction": winTestJunction,
		"symlink":  winTestSymlink,
	}
	for name, mk := range aliases {
		t.Run(name, func(t *testing.T) {
			link := filepath.Join(t.TempDir(), "alias")
			mk(t, link, winDir)
			for _, path := range []string{
				link,
				filepath.Join(link, "System32"),
				filepath.Join(link, "System32", "not-yet-created", ".beads"),
			} {
				if isPathInSafeBoundary(path) {
					t.Errorf("isPathInSafeBoundary(%q) = true, want false (%s into %s)", path, name, winDir)
				}
			}
		})
	}
}

// winTestJunction creates a directory junction and removes the junction
// itself (never its target) when the test ends.
func winTestJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if _, lerr := os.Lstat(link); err != nil || lerr != nil {
		t.Skipf("cannot create junction: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// winTestSymlink creates a directory symlink (needs Developer Mode or the
// symlink privilege) and removes the link itself when the test ends.
func winTestSymlink(t *testing.T, link, target string) {
	t.Helper()
	err := os.Symlink(target, link)
	if _, lerr := os.Lstat(link); err != nil || lerr != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// winTestShortName returns the 8.3 short name of an existing path, skipping
// when the volume does not generate one.
func winTestShortName(t *testing.T, long string) string {
	t.Helper()
	long16, err := windows.UTF16FromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(&long16[0], &buf[0], uint32(len(buf)))
	short := windows.UTF16ToString(buf[:n])
	if err != nil || strings.EqualFold(short, long) {
		t.Skipf("no 8.3 name for %q: %v", long, err)
	}
	return short
}

// winTestSamePath compares a resolver result with an expected path.
func winTestSamePath(t *testing.T, got, want string) {
	t.Helper()
	g, gok := winCleanPath(got)
	w, wok := winCleanPath(want)
	if !gok || !wok || !g.within(w) || !w.within(g) {
		t.Errorf("resolved %q, want %q", got, want)
	}
}

// TestResolveWindowsFinalPath exercises the host resolver against the real
// filesystem: links are followed, short names expand, and missing entries
// are told apart from dangling links.
func TestResolveWindowsFinalPath(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "a long directory name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, isDir, err := resolveWindowsFinalPath(dir)
	if err != nil || !isDir {
		t.Fatalf("resolve(%q) = %q, %v, %v; want a directory", dir, got, isDir, err)
	}
	winTestSamePath(t, got, dir)

	got, isDir, err = resolveWindowsFinalPath(file)
	if err != nil || isDir {
		t.Fatalf("resolve(%q) = %q, %v, %v; want a file", file, got, isDir, err)
	}
	winTestSamePath(t, got, file)

	if _, _, err := resolveWindowsFinalPath(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("resolve(missing) error = %v, want fs.ErrNotExist", err)
	}

	t.Run("short name", func(t *testing.T) {
		short := winTestShortName(t, dir)
		got, _, err := resolveWindowsFinalPath(short)
		if err != nil {
			t.Fatalf("resolve(%q): %v", short, err)
		}
		winTestSamePath(t, got, dir)
	})

	for name, mk := range map[string]func(t *testing.T, link, target string){
		"junction": winTestJunction,
		"symlink":  winTestSymlink,
	} {
		t.Run(name, func(t *testing.T) {
			link := filepath.Join(t.TempDir(), "link")
			mk(t, link, dir)
			got, isDir, err := resolveWindowsFinalPath(link)
			if err != nil || !isDir {
				t.Fatalf("resolve(%q) = %q, %v, %v; want the target directory", link, got, isDir, err)
			}
			winTestSamePath(t, got, dir)

			// The boundary's walk carries a missing tail onto the link's
			// target and refuses a tail below a file.
			b := &windowsBoundary{resolve: resolveWindowsFinalPath}
			p, _ := winCleanPath(filepath.Join(link, "new", ".beads"))
			phys, ok := b.physical(p)
			if !ok {
				t.Fatalf("physical(%q) unresolved", p)
			}
			winTestSamePath(t, phys.String(), filepath.Join(dir, "new", ".beads"))
			p, _ = winCleanPath(filepath.Join(link, "file.txt", "x"))
			if phys, ok := b.physical(p); ok {
				t.Errorf("physical(%q) = %q, want unresolved (file ancestor)", p, phys)
			}

			// Dangling once the target is gone.
			target := filepath.Join(root, name+"-target")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			dangling := filepath.Join(t.TempDir(), "dangling")
			mk(t, dangling, target)
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if _, _, err := resolveWindowsFinalPath(dangling); !errors.Is(err, errWinDanglingLink) {
				t.Errorf("resolve(dangling) error = %v, want errWinDanglingLink", err)
			}
			p, _ = winCleanPath(filepath.Join(dangling, "new"))
			if phys, ok := b.physical(p); ok {
				t.Errorf("physical(%q) = %q, want unresolved (dangling link)", p, phys)
			}
		})
	}
}

// TestGetProfilesDirectory checks the private userenv wrapper against the
// profile parent of the account's home.
func TestGetProfilesDirectory(t *testing.T) {
	dir, err := getProfilesDirectory()
	if err != nil || dir == "" {
		t.Fatalf("getProfilesDirectory() = %q, %v", dir, err)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		if !strings.EqualFold(filepath.Dir(u.HomeDir), dir) {
			t.Logf("home %q is not directly under the profiles directory %q", u.HomeDir, dir)
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
