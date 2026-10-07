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
