package workspacegate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// setFlockSeams swaps the gate's non-blocking lock primitives for the test.
func setFlockSeams(t *testing.T, shared, exclusive func(*os.File) error) {
	t.Helper()
	oldShared, oldExclusive := flockSharedNB, flockExclusiveNB
	flockSharedNB, flockExclusiveNB = shared, exclusive
	t.Cleanup(func() { flockSharedNB, flockExclusiveNB = oldShared, oldExclusive })
}

// countingBusy reports busy for every file, as Windows' 9P redirector can
// for \\wsl$ paths, and counts the calls that were made on a probe file.
func countingBusy(probes *atomic.Int32) func(*os.File) error {
	return func(f *os.File) error {
		if strings.Contains(filepath.Base(f.Name()), ".probe-") {
			probes.Add(1)
		}
		return lockfile.ErrLockBusy
	}
}

func probeFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A lock that reports busy with no holder must not cost a command its whole
// wait budget and then blame a maintenance operation that is not running.
func TestAcquireLyingBusyFilesystemIsUnsupported(t *testing.T) {
	for _, mode := range []Mode{Shared, Exclusive} {
		for _, staleIntent := range []bool{false, true} {
			name := mode.String()
			if staleIntent {
				name += " with leftover intent file"
			}
			t.Run(name, func(t *testing.T) {
				var probes atomic.Int32
				setFlockSeams(t, countingBusy(&probes), countingBusy(&probes))
				g, dir := testGate(t)
				if staleIntent {
					// A failed bd init leaves its intent file behind; on a
					// lying filesystem it then reads as a queued writer.
					if err := os.WriteFile(g.intentPath(), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				waited := false
				start := time.Now()
				h, err := g.Acquire(context.Background(), mode, Options{
					Wait:   2 * time.Second,
					OnWait: func(string) { waited = true },
				})
				elapsed := time.Since(start)
				if h != nil {
					_ = h.Release()
				}
				if !errors.Is(err, lockfile.ErrUnsupported) {
					t.Fatalf("Acquire = %v, want lockfile.ErrUnsupported", err)
				}
				var unsupported *lockfile.UnsupportedError
				if !errors.As(err, &unsupported) || unsupported.Path != g.Path() {
					t.Errorf("Acquire = %v, want an UnsupportedError naming the gate %s", err, g.Path())
				}
				if errors.Is(err, ErrBusy) {
					t.Errorf("Acquire = %v, must not read as ErrBusy", err)
				}
				if elapsed >= time.Second {
					t.Errorf("Acquire took %s, want well under the 2s wait", elapsed)
				}
				if waited {
					t.Error("OnWait called for a filesystem that cannot lock")
				}
				if got := probes.Load(); got != 1 {
					t.Errorf("probe ran %d times, want once", got)
				}
				if left := probeFiles(t, dir); len(left) != 0 {
					t.Errorf("probe files left behind: %v", left)
				}
			})
		}
	}

	t.Run("uncontended acquire never probes", func(t *testing.T) {
		var probes atomic.Int32
		ok := func(f *os.File) error {
			if strings.Contains(filepath.Base(f.Name()), ".probe-") {
				probes.Add(1)
			}
			return nil
		}
		setFlockSeams(t, ok, ok)
		g, dir := testGate(t)
		for _, mode := range []Mode{Shared, Exclusive} {
			h := mustAcquire(t, g, mode, Options{Wait: time.Second})
			_ = h.Release()
		}
		if got := probes.Load(); got != 0 {
			t.Errorf("probe ran %d times on uncontended acquires", got)
		}
		if left := probeFiles(t, dir); len(left) != 0 {
			t.Errorf("probe files left behind: %v", left)
		}
	})

	t.Run("fail-fast shared", func(t *testing.T) {
		var probes atomic.Int32
		setFlockSeams(t, countingBusy(&probes), countingBusy(&probes))
		g, _ := testGate(t)
		_, err := g.Acquire(context.Background(), Shared, Options{})
		if !errors.Is(err, lockfile.ErrUnsupported) || errors.Is(err, ErrBusy) {
			t.Fatalf("fail-fast Acquire = %v, want lockfile.ErrUnsupported and not ErrBusy", err)
		}
	})
}

// A lock call that fails outright (ENOLCK, ENOTSUP, Windows' "Incorrect
// function") returns at once in either mode: no probe, no wait, and the error
// names the gate whose filesystem cannot lock.
func TestAcquireFailingLockCallIsUnsupported(t *testing.T) {
	var probes atomic.Int32
	fail := func(f *os.File) error {
		if strings.Contains(filepath.Base(f.Name()), ".probe-") {
			probes.Add(1)
		}
		return &lockfile.UnsupportedError{Op: "flock", Err: errors.ErrUnsupported}
	}
	setFlockSeams(t, fail, fail)
	for _, mode := range []Mode{Shared, Exclusive} {
		t.Run(mode.String(), func(t *testing.T) {
			g, dir := testGate(t)
			waited := false
			start := time.Now()
			h, err := g.Acquire(context.Background(), mode, Options{
				Wait:   2 * time.Second,
				OnWait: func(string) { waited = true },
			})
			elapsed := time.Since(start)
			if h != nil {
				_ = h.Release()
			}
			var unsupported *lockfile.UnsupportedError
			if !errors.As(err, &unsupported) || unsupported.Path != g.Path() {
				t.Fatalf("Acquire = %v, want an UnsupportedError naming the gate %s", err, g.Path())
			}
			if !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("Acquire = %v, lost the lock call's cause", err)
			}
			if errors.Is(err, ErrBusy) {
				t.Errorf("Acquire = %v, must not read as ErrBusy", err)
			}
			if elapsed >= time.Second {
				t.Errorf("Acquire took %s, want well under the 2s wait", elapsed)
			}
			if waited {
				t.Error("OnWait called for a lock call that failed outright")
			}
			if got := probes.Load(); got != 0 {
				t.Errorf("probe ran %d times, want none", got)
			}
			if left := probeFiles(t, dir); len(left) != 0 {
				t.Errorf("probe files left behind: %v", left)
			}
		})
	}
}

// The probe must not turn real contention into "unsupported": on a local
// filesystem the private probe file locks, so a real holder still makes a
// waiting shared acquirer give up with ErrBusy.
func TestAcquireRealContentionStaysBusy(t *testing.T) {
	var probes atomic.Int32
	counted := func(lock func(*os.File) error) func(*os.File) error {
		return func(f *os.File) error {
			if strings.Contains(filepath.Base(f.Name()), ".probe-") {
				probes.Add(1)
			}
			return lock(f)
		}
	}
	setFlockSeams(t, counted(lockfile.FlockSharedNonBlock), counted(lockfile.FlockExclusiveNonBlock))
	g, dir := testGate(t)

	// Another open file description holds the gate exclusively.
	holder, err := os.OpenFile(g.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockfile.FlockExclusiveNonBlock(holder); err != nil {
		t.Fatalf("holder lock: %v", err)
	}
	defer func() { _ = lockfile.FlockUnlock(holder) }()

	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared, Options{Wait: 300 * time.Millisecond})
	if h != nil {
		_ = h.Release()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Acquire under a real holder = %v, want ErrBusy", err)
	}
	if errors.Is(err, lockfile.ErrUnsupported) {
		t.Errorf("Acquire under a real holder = %v, must not read as unsupported", err)
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("gave up after %s, want the 300ms wait spent", waited)
	}
	if got := probes.Load(); got != 1 {
		t.Errorf("probe ran %d times, want once", got)
	}
	if left := probeFiles(t, dir); len(left) != 0 {
		t.Errorf("probe files left behind: %v", left)
	}
}

// Strict readonly promises to create nothing (#7672), so it never runs the
// probe: a busy existing gate gives the same ErrBusy as before the probe.
func TestAcquireReadOnlyBusyCreatesNoProbe(t *testing.T) {
	var probes atomic.Int32
	setFlockSeams(t, countingBusy(&probes), countingBusy(&probes))
	g, dir := testGate(t)
	if err := os.WriteFile(g.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := gateDirEntries(t, dir)

	h, err := g.Acquire(context.Background(), Shared, Options{ReadOnly: true, Wait: 300 * time.Millisecond})
	if h != nil {
		_ = h.Release()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("readonly Acquire on a busy gate = %v, want ErrBusy", err)
	}
	if errors.Is(err, lockfile.ErrUnsupported) {
		t.Errorf("readonly Acquire = %v, must not have probed", err)
	}
	if got := probes.Load(); got != 0 {
		t.Errorf("readonly Acquire ran the probe %d times", got)
	}
	if after := gateDirEntries(t, dir); !slices.Equal(before, after) {
		t.Errorf("readonly Acquire changed the directory: %v -> %v", before, after)
	}
}
