//go:build windows

package workspacegate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// makeJunction creates a directory junction with mklink /J (no privileges
// required) and fails the test when the tooling cannot create one: these
// tests exist to prove gate identity beneath reparse points — the same
// reparse traversal a directory-mounted volume needs — not to skip past
// them.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J %s %s: %v: %s", link, target, err, out)
	}
}

// junctionFixture builds <tmp>\real\proj and the junction <tmp>\j ->
// <tmp>\real, the minimal reproduction of the directory-mounted-volume
// ancestry that broke ForWorkspace before handle-based resolution.
type junctionFixture struct {
	tmp, real, proj, junction string
}

func newJunctionFixture(t *testing.T) *junctionFixture {
	t.Helper()
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	proj := filepath.Join(real, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(tmp, "j")
	makeJunction(t, junction, real)
	return &junctionFixture{tmp: tmp, real: real, proj: proj, junction: junction}
}

// TestForDirBeneathJunctionWindows: building a gate through a junction
// must succeed (no "gate parent must exist") and land on the physical
// directory's spelling, exactly as the direct spelling does. Before
// handle-based resolution this failed with the reported
// "The system cannot find the path specified." without needing admin
// rights to see.
func TestForDirBeneathJunctionWindows(t *testing.T) {
	f := newJunctionFixture(t)
	want := filepath.Join(mustEval(t, f.proj), ".beads.gate.lock")

	g, err := ForWorkspace(filepath.Join(f.junction, "proj", ".beads"))
	if err != nil {
		t.Fatalf("ForWorkspace through junction: %v", err)
	}
	if g.Path() != want {
		t.Fatalf("gate path through junction = %s, want %s", g.Path(), want)
	}
	pr, err := ForPhysicalRoot(filepath.Join(f.junction, "proj", ".beads"))
	if err != nil {
		t.Fatalf("ForPhysicalRoot through junction: %v", err)
	}
	if pr.Path() != want {
		t.Fatalf("physical-root gate path through junction = %s, want %s", pr.Path(), want)
	}

	// A parent longer than MAX_PATH through the junction: the gate path
	// keeps its plain spelling (no \\?\ prefix) and stays lockable.
	t.Run("long parent", func(t *testing.T) {
		long := f.proj
		for len(long) < 280 {
			long = filepath.Join(long, "deep-component")
		}
		if err := os.MkdirAll(long, 0o755); err != nil {
			t.Fatal(err)
		}
		through := strings.Replace(long, f.real, f.junction, 1)
		if len(through) < 260 {
			t.Fatalf("fixture bug: junction spelling %s is not a long path", through)
		}
		g, err := ForWorkspace(filepath.Join(through, ".beads"))
		if err != nil {
			t.Fatalf("ForWorkspace beneath long junction path: %v", err)
		}
		if strings.HasPrefix(g.Path(), `\\?\`) {
			t.Fatalf("gate path %s keeps the extended-length prefix", g.Path())
		}
		if want := filepath.Join(mustEval(t, long), ".beads.gate.lock"); g.Path() != want {
			t.Fatalf("gate path = %s, want %s", g.Path(), want)
		}
		h := mustAcquire(t, g, Exclusive, Options{})
		if err := h.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})

	// Forward slashes and an explicit \\?\ prefix are just spellings of
	// the same parent: both resolve to the same gate file.
	t.Run("alternate spellings", func(t *testing.T) {
		fwd := strings.ReplaceAll(filepath.Join(f.junction, "proj", ".beads"), `\`, `/`)
		g, err := ForWorkspace(fwd)
		if err != nil {
			t.Fatalf("ForWorkspace with forward slashes: %v", err)
		}
		if g.Path() != want {
			t.Fatalf("forward-slash gate path = %s, want %s", g.Path(), want)
		}
		ext := `\\?\` + filepath.Join(f.junction, "proj", ".beads")
		g2, err := ForWorkspace(ext)
		if err != nil {
			t.Fatalf("ForWorkspace with extended-length prefix: %v", err)
		}
		if g2.Path() != want {
			t.Fatalf("extended-length gate path = %s, want %s", g2.Path(), want)
		}
	})

	// A junction whose target is spelled with the volume GUID (the
	// spelling GetVolumeNameForVolumeMountPoint reports) still resolves
	// to the DOS spelling of the same physical directory: the volume HAS
	// a drive letter, so DOS wins by design.
	t.Run("volume GUID junction target", func(t *testing.T) {
		root := filepath.VolumeName(f.real) + `\`
		buf := make([]uint16, 64)
		if err := windows.GetVolumeNameForVolumeMountPoint(windows.StringToUTF16Ptr(root), &buf[0], uint32(len(buf))); err != nil {
			t.Fatalf("GetVolumeNameForVolumeMountPoint(%s): %v", root, err)
		}
		vol := windows.UTF16ToString(buf) // \\?\Volume{...}\ (trailing separator)
		rel := strings.TrimPrefix(f.real, filepath.VolumeName(f.real))
		guidTarget := vol + rel[1:]
		junction := filepath.Join(f.tmp, "jv")
		makeJunction(t, junction, guidTarget)
		g, err := ForWorkspace(filepath.Join(junction, "proj", ".beads"))
		if err != nil {
			t.Fatalf("ForWorkspace through volume-GUID junction: %v", err)
		}
		if g.Path() != want {
			t.Fatalf("volume-GUID junction gate path = %s, want %s", g.Path(), want)
		}
	})
}

// TestJunctionAliasesShareOneGateWindows: every spelling of one physical
// parent — through the junction, directly, upper-cased, and a parent that
// is itself the junction versus the directory it points to — must produce
// one gate file, so AcquireAll sees (and dedupes to) a single lock. Before
// handle-based resolution the junction spellings either errored (junction
// mid-path) or silently keyed a second gate (junction as the parent's last
// component).
func TestJunctionAliasesShareOneGateWindows(t *testing.T) {
	f := newJunctionFixture(t)

	t.Run("parent beneath the junction", func(t *testing.T) {
		guarded := []string{
			filepath.Join(f.junction, "proj", ".beads"),
			filepath.Join(f.real, "proj", ".beads"),
			filepath.Join(strings.ToUpper(f.real), strings.ToUpper("proj"), ".beads"),
		}
		want := filepath.Join(mustEval(t, f.proj), ".beads.gate.lock")
		assertOneGateOneLock(t, want, guarded)
	})

	t.Run("parent that is the junction itself", func(t *testing.T) {
		guarded := []string{
			filepath.Join(f.junction, ".beads"),
			filepath.Join(f.real, ".beads"),
		}
		want := filepath.Join(mustEval(t, f.real), ".beads.gate.lock")
		assertOneGateOneLock(t, want, guarded)
	})
}

// assertOneGateOneLock builds a gate per guarded spelling, requires every
// Path() to be identical, then takes the whole set exclusively in one
// AcquireAll and requires it to dedupe to a single held lock.
func assertOneGateOneLock(t *testing.T, want string, guarded []string) {
	t.Helper()
	gates := make([]Gate, 0, len(guarded))
	for _, p := range guarded {
		g, err := ForWorkspace(p)
		if err != nil {
			t.Fatalf("ForWorkspace(%s): %v", p, err)
		}
		if g.Path() != want {
			t.Fatalf("ForWorkspace(%s).Path() = %s, want %s", p, g.Path(), want)
		}
		gates = append(gates, g)
	}
	m, err := AcquireAll(context.Background(), Exclusive, Options{}, gates...)
	if err != nil {
		t.Fatalf("AcquireAll over alias spellings: %v", err)
	}
	defer m.Release()
	if len(m.handles) != 1 {
		t.Fatalf("alias spellings held %d locks, want 1 (dedupe by canonical path)", len(m.handles))
	}
}
