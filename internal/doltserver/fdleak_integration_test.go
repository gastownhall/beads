//go:build integration && linux

package doltserver_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestStart_ServerDoesNotInheritCallerFDs is the end-to-end regression test for
// GH#4634: a caller that holds a descriptor without FD_CLOEXEC and cold-starts
// the sql-server used to have that descriptor pinned by the server for its
// whole lifetime, which outlives the caller. The reported symptom was a sync
// script's flock on fd 9 reading as permanently held.
//
// Linux-only because it reads the child's fd table from /proc/<pid>/fd. The
// mechanism itself is unix-wide and is unit-tested per-platform in
// internal/fdhygiene.
func TestStart_ServerDoesNotInheritCallerFDs(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Stand in for the caller's leaked descriptor: opened without O_CLOEXEC,
	// exactly as a shell's `exec 9>lockfile` leaves it.
	leakPath := filepath.Join(t.TempDir(), "caller.lock")
	if err := os.WriteFile(leakPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding %s: %v", leakPath, err)
	}
	leakFD, err := unix.Open(leakPath, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s without O_CLOEXEC: %v", leakPath, err)
	}
	defer func() { _ = unix.Close(leakFD) }()

	flags, err := unix.FcntlInt(uintptr(leakFD), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD on fd %d: %v", leakFD, err)
	}
	if flags&unix.FD_CLOEXEC != 0 {
		t.Fatalf("fixture fd %d is already close-on-exec; the test would pass vacuously", leakFD)
	}

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, findErr := os.FindProcess(state.PID); findErr == nil {
		reg.Register(p)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })

	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", leakFD))
	if err != nil {
		t.Fatalf("resolving own fd %d: %v", leakFD, err)
	}

	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", state.PID))
	if err != nil {
		t.Fatalf("reading fd table of server pid %d: %v", state.PID, err)
	}
	for _, e := range entries {
		link, rlErr := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", state.PID, e.Name()))
		if rlErr != nil {
			continue // races with the server closing an fd are expected
		}
		if strings.HasSuffix(link, target) {
			t.Errorf("sql-server pid %d inherited the caller's descriptor: fd %s -> %s",
				state.PID, e.Name(), link)
		}
	}
}
