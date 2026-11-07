//go:build windows

package workspacegate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVolumeGUIDParent(t *testing.T) {
	dir := t.TempDir()
	physical, err := canonicalParent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(physical, `\\?\Volume{`) {
		t.Fatalf("local parent = %q, want a volume GUID path", physical)
	}
	alias := filepath.Join(dir, "mounted")
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, physical).CombinedOutput(); err != nil {
		t.Fatalf("creating volume-GUID junction: %v\n%s", err, output)
	}
	// Do not recurse through the junction when removing the fixture.
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil {
			t.Error(err)
		}
	})
	child := filepath.Join(dir, "workspace")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	g1, err := ForWorkspace(filepath.Join(child, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	g2, err := ForWorkspace(filepath.Join(alias, "workspace", ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	g3, err := ForWorkspace(filepath.Join(physical, "workspace", ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	g4, err := ForPhysicalRoot(filepath.Join(alias, "workspace", ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	if g1.Path() != g2.Path() || g1.Path() != g3.Path() || g1.Path() != g4.Path() {
		t.Fatalf("aliases disagree: %q, %q, %q, %q", g1.Path(), g2.Path(), g3.Path(), g4.Path())
	}
	h := mustAcquire(t, g1, Exclusive, Options{})
	defer h.Release()
	for _, gate := range []Gate{g2, g3, g4} {
		if _, err := gate.Acquire(context.Background(), Exclusive, Options{}); !errors.Is(err, ErrBusy) {
			t.Fatalf("alias acquisition = %v, want ErrBusy", err)
		}
	}
	if err := os.Mkdir(filepath.Join(child, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	created, err := ForWorkspace(filepath.Join(alias, "workspace", ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Path() != g1.Path() {
		t.Fatalf("creating .beads changed gate: %q != %q", created.Path(), g1.Path())
	}
}

func TestCanonicalParentLongPathWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, strings.Repeat("a", 120), strings.Repeat("b", 120))
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalParent(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(mustEval(t, dir), strings.Repeat("a", 120), strings.Repeat("b", 120))
	if got != want {
		t.Fatalf("long parent = %q, want %q", got, want)
	}
}

func TestCanonicalParentMissingWindows(t *testing.T) {
	if _, err := canonicalParent(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent = %v, want not-exist error", err)
	}
}
