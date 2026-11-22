package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinSuiteTempRootRedirectsMkdirTemp(t *testing.T) {
	oldGOTMPDIR := os.Getenv("GOTMPDIR")
	oldTMPDIR := os.Getenv("TMPDIR")
	oldTMP := os.Getenv("TMP")
	oldTEMP := os.Getenv("TEMP")
	t.Cleanup(func() {
		_ = os.Setenv("GOTMPDIR", oldGOTMPDIR)
		_ = os.Setenv("TMPDIR", oldTMPDIR)
		_ = os.Setenv("TMP", oldTMP)
		_ = os.Setenv("TEMP", oldTEMP)
	})

	root, err := PinSuiteTempRoot("beads-pin-suite-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	if os.Getenv("GOTMPDIR") != root {
		t.Fatalf("GOTMPDIR=%q, want pinned root %q", os.Getenv("GOTMPDIR"), root)
	}
	child, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), "child-*")
	if err != nil {
		t.Fatal(err)
	}
	if !isUnderRoot(child, root) {
		t.Fatalf("GOTMPDIR MkdirTemp child %q is not under pinned root %q", child, root)
	}
}

// A path that does not exist yet has no symlinks of its own to resolve, but
// its parents may: macOS hands out /var/... for /private/var/.... The
// predicate has to spell such a path the way it spells an existing root, or a
// path under the root is reported as outside it.
func TestPathUnderSuiteRootResolvesMissingPathThroughSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	for _, tc := range []struct {
		name      string
		dir, root string
		want      bool
	}{
		{"existing root via the link", link, target, true},
		{"missing child via the link", filepath.Join(link, "x"), target, true},
		{"missing nested path via the link", filepath.Join(link, "a", "b", "c"), target, true},
		{"missing child, root via the link", filepath.Join(target, "x"), link, true},
		{"missing path under a sibling of the link", filepath.Join(link+"-other", "x"), target, false},
		{"missing path under a sibling of the target", filepath.Join(target+"-other", "x"), link, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PathUnderSuiteRoot(tc.dir, tc.root); got != tc.want {
				t.Errorf("PathUnderSuiteRoot(%q, %q) = %v, want %v", tc.dir, tc.root, got, tc.want)
			}
		})
	}
}

func isUnderRoot(dir, root string) bool {
	dir = resolve(dir)
	root = resolve(root)
	if dir == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(dir, strings.TrimRight(root, sep)+sep)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
