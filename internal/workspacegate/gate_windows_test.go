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
	"time"

	"golang.org/x/sys/windows"
)

// Native Windows tests for gate-parent resolution through mount-point
// reparse points. Junctions (mklink /J) need no privileges and take the
// same EvalSymlinks-refusing path as a directory-mounted volume such as a
// Dev Drive, so they stand in for one. A fixture that cannot be built is a
// failure, never a skip.

// mklinkJunction creates the junction link -> target and removes it (the
// link only, never the target's contents) before the temp dir is cleaned.
func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("removing junction %s: %v", link, err)
		}
	})
}

// junctionFixture builds <tmp>\real\proj and the junction <tmp>\j ->
// <tmp>\real.
type junctionFixture struct {
	tmp, real, realProj, j, jProj string
}

func newJunctionFixture(t *testing.T) junctionFixture {
	t.Helper()
	tmp := t.TempDir()
	f := junctionFixture{
		tmp:      tmp,
		real:     filepath.Join(tmp, "real"),
		realProj: filepath.Join(tmp, "real", "proj"),
		j:        filepath.Join(tmp, "j"),
		jProj:    filepath.Join(tmp, "j", "proj"),
	}
	if err := os.MkdirAll(f.realProj, 0o700); err != nil {
		t.Fatal(err)
	}
	mklinkJunction(t, f.j, f.real)
	return f
}

func TestForDirBeneathJunctionWindows(t *testing.T) {
	f := newJunctionFixture(t)
	canonProj := mustEval(t, f.realProj)
	want := filepath.Join(canonProj, ".beads.gate.lock")

	assertGate := func(t *testing.T, beadsDir, want string) Gate {
		t.Helper()
		gw, err := ForWorkspace(beadsDir)
		if err != nil {
			t.Fatalf("ForWorkspace(%s): %v", beadsDir, err)
		}
		gp, err := ForPhysicalRoot(beadsDir)
		if err != nil {
			t.Fatalf("ForPhysicalRoot(%s): %v", beadsDir, err)
		}
		for _, g := range []Gate{gw, gp} {
			if g.Path() != want {
				t.Fatalf("gate path for %s = %s, want %s", beadsDir, g.Path(), want)
			}
			if strings.HasPrefix(g.Path(), `\\?\`) {
				t.Fatalf("gate path %s carries the \\\\?\\ prefix", g.Path())
			}
		}
		return gw
	}

	t.Run("through junction", func(t *testing.T) {
		assertGate(t, filepath.Join(f.jProj, ".beads"), want)
	})

	t.Run("forward slashes", func(t *testing.T) {
		assertGate(t, filepath.ToSlash(f.jProj)+"/.beads", want)
	})

	t.Run("extended-length prefix", func(t *testing.T) {
		assertGate(t, `\\?\`+filepath.Join(f.jProj, ".beads"), want)
	})

	t.Run("long parent", func(t *testing.T) {
		a, b := strings.Repeat("a", 120), strings.Repeat("b", 120)
		if err := os.MkdirAll(filepath.Join(f.realProj, a, b), 0o700); err != nil {
			t.Fatal(err)
		}
		beadsDir := filepath.Join(f.jProj, a, b, ".beads")
		if len(filepath.Dir(beadsDir)) <= windows.MAX_PATH {
			t.Fatalf("fixture parent %s is not longer than MAX_PATH", filepath.Dir(beadsDir))
		}
		g := assertGate(t, beadsDir, filepath.Join(canonProj, a, b, ".beads.gate.lock"))
		h := mustAcquire(t, g, Exclusive, Options{})
		if err := h.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})

	t.Run("junction to volume GUID root", func(t *testing.T) {
		mountPoint := volumeQuery(t, "GetVolumePathName", canonProj, windows.GetVolumePathName)
		volRoot := volumeQuery(t, "GetVolumeNameForVolumeMountPoint", mountPoint, windows.GetVolumeNameForVolumeMountPoint)
		if !strings.HasPrefix(volRoot, `\\?\Volume{`) {
			t.Fatalf("volume name %q is not a volume GUID path", volRoot)
		}
		if len(canonProj) < len(mountPoint) || !strings.EqualFold(canonProj[:len(mountPoint)], mountPoint) {
			t.Fatalf("%s is not under its volume mount point %s", canonProj, mountPoint)
		}
		v := filepath.Join(f.tmp, "v")
		mklinkJunction(t, v, volRoot)
		assertGate(t, filepath.Join(v, canonProj[len(mountPoint):], ".beads"), want)
	})
}

// volumeQuery runs a Win32 volume query that fills a UTF-16 buffer from
// a path argument.
func volumeQuery(t *testing.T, name, arg string, query func(in, out *uint16, n uint32) error) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(arg)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, arg, err)
	}
	out := make([]uint16, windows.MAX_PATH)
	if err := query(in, &out[0], uint32(len(out))); err != nil {
		t.Fatalf("%s(%s): %v", name, arg, err)
	}
	return windows.UTF16ToString(out)
}

// Every spelling of one physical parent selects one gate file, so
// AcquireAll's dedupe and sort agree with the OS lock. A parent that is
// itself the junction is the case EvalSymlinks returned unevaluated.
func TestJunctionAliasesShareOneGateWindows(t *testing.T) {
	f := newJunctionFixture(t)
	for _, tc := range []struct {
		name    string
		parents []string
	}{
		{"parent beneath junction", []string{f.jProj, f.realProj, strings.ToUpper(f.realProj), strings.ToUpper(f.jProj)}},
		{"parent is the junction", []string{f.j, f.real, strings.ToUpper(f.j)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gates := make([]Gate, 0, len(tc.parents))
			for _, p := range tc.parents {
				g, err := ForWorkspace(filepath.Join(p, ".beads"))
				if err != nil {
					t.Fatalf("ForWorkspace(%s): %v", p, err)
				}
				if len(gates) > 0 && g.Path() != gates[0].Path() {
					t.Fatalf("spelling %s selected gate %s, %s selected %s", p, g.Path(), tc.parents[0], gates[0].Path())
				}
				gates = append(gates, g)
			}
			m, err := AcquireAll(context.Background(), Exclusive, Options{}, gates...)
			if err != nil {
				t.Fatalf("AcquireAll over aliases of one gate: %v", err)
			}
			defer func() { _ = m.Release() }()
			if len(m.handles) != 1 {
				t.Fatalf("AcquireAll held %d locks for one physical gate, want 1", len(m.handles))
			}
		})
	}
}

// A holder in another process that reached the workspace through one
// spelling excludes this process reaching it through the other.
func TestCrossProcessExclusionAcrossJunctionWindows(t *testing.T) {
	f := newJunctionFixture(t)
	for _, tc := range []struct {
		name          string
		holder, local string
	}{
		{"holder via junction", f.jProj, f.realProj},
		{"holder via target", f.realProj, f.jProj},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := ForWorkspace(filepath.Join(tc.local, ".beads"))
			if err != nil {
				t.Fatal(err)
			}
			hp := spawnHolder(t, tc.holder, "exclusive")
			if _, err := g.Acquire(context.Background(), Shared, Options{}); !errors.Is(err, ErrBusy) {
				t.Fatalf("shared via %s while another process holds exclusive via %s: %v, want ErrBusy", tc.local, tc.holder, err)
			}
			hp.stop()
			h, err := g.Acquire(context.Background(), Shared,
				Options{Wait: 10 * time.Second, PollInterval: 25 * time.Millisecond})
			if err != nil {
				t.Fatalf("shared after the holder exited: %v", err)
			}
			_ = h.Release()
		})
	}
}

// A plain directory keeps the drive-letter gate path EvalSymlinks gave it
// before handle-based resolution, so existing gate files do not move.
func TestPlainParentKeepsDriveLetterSpellingWindows(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(mustEval(t, dir), ".beads.gate.lock")
	if g.Path() != want {
		t.Fatalf("gate path = %s, want %s", g.Path(), want)
	}
	if vol := filepath.VolumeName(g.Path()); len(vol) != 2 || vol[1] != ':' {
		t.Fatalf("gate path %s has volume %q, want a drive letter and no \\\\?\\ prefix", g.Path(), vol)
	}
}
