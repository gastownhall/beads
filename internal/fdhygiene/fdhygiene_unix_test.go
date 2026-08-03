//go:build unix

package fdhygiene

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// openLeakedFD opens a file the way a caller outside Go would leave one: no
// O_CLOEXEC, so it survives an exec. Returns the raw descriptor.
func openLeakedFD(t *testing.T) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leak.lock")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding %s: %v", path, err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s without O_CLOEXEC: %v", path, err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })

	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD on fd %d: %v", fd, err)
	}
	if flags&unix.FD_CLOEXEC != 0 {
		t.Fatalf("fd %d unexpectedly opened close-on-exec; the fixture proves nothing", fd)
	}
	return fd
}

func TestMarkInheritedCloexec_MarksLeakedFD(t *testing.T) {
	fd := openLeakedFD(t)

	marked := MarkInheritedCloexec()

	var found bool
	for _, m := range marked {
		if m == fd {
			found = true
		}
	}
	if !found {
		t.Errorf("fd %d not reported as marked; got %v", fd, marked)
	}

	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD on fd %d: %v", fd, err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("fd %d still lacks FD_CLOEXEC after marking", fd)
	}
}

// TestMarkInheritedCloexec_LeavesStdioAlone guards the carve-out: os/exec
// rewires 0/1/2 per-child, so marking bd's own stdio would change unrelated
// spawns.
func TestMarkInheritedCloexec_LeavesStdioAlone(t *testing.T) {
	before := make([]int, 3)
	for fd := range before {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Skipf("stdio fd %d not open under this test runner: %v", fd, err)
		}
		before[fd] = flags
	}

	for _, fd := range MarkInheritedCloexec() {
		if fd <= 2 {
			t.Errorf("MarkInheritedCloexec reported stdio fd %d as marked", fd)
		}
	}

	for fd, want := range before {
		got, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Fatalf("F_GETFD on stdio fd %d: %v", fd, err)
		}
		if got != want {
			t.Errorf("stdio fd %d flags changed: %d -> %d", fd, want, got)
		}
	}
}

// TestMarkInheritedCloexec_Idempotent checks the second call is a no-op, which
// is what makes calling it on every spawn cheap and safe.
func TestMarkInheritedCloexec_Idempotent(t *testing.T) {
	openLeakedFD(t)

	if first := MarkInheritedCloexec(); len(first) == 0 {
		t.Fatal("first call marked nothing; fixture fd was not seen")
	}
	if second := MarkInheritedCloexec(); len(second) != 0 {
		t.Errorf("second call marked %v, want nothing left to mark", second)
	}
}

// TestMarkInheritedCloexec_ChildDoesNotInherit is the regression test for
// GH#4634 proper: it execs a real child and asserts the descriptor is absent
// from the child's fd table. Without the fix the same child sees it.
func TestMarkInheritedCloexec_ChildDoesNotInherit(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh on PATH: %v", err)
	}

	fd := openLeakedFD(t)

	// Baseline: the child inherits it today. This is the bug, asserted first so
	// a future runtime that closes fds itself makes the test fail loudly rather
	// than pass vacuously.
	if !childSeesFD(t, sh, fd) {
		t.Skipf("fd %d does not reach an unsanitized child on this platform; nothing to regress", fd)
	}

	MarkInheritedCloexec()

	if childSeesFD(t, sh, fd) {
		t.Errorf("fd %d still present in the child's fd table after marking", fd)
	}
}

// childSeesFD execs a child that lists its own open descriptors and reports
// whether fd appears. /dev/fd is the portable spelling: a real fdescfs on
// darwin and the BSDs, a symlink to /proc/self/fd on Linux.
func childSeesFD(t *testing.T, sh string, fd int) bool {
	t.Helper()
	out, err := exec.Command(sh, "-c", "ls /dev/fd").Output() //nolint:gosec // G204: sh is PATH-resolved in-test, args are constant
	if err != nil {
		t.Fatalf("listing child fds: %v", err)
	}
	want := strconv.Itoa(fd)
	for _, f := range strings.Fields(string(out)) {
		if f == want {
			return true
		}
	}
	return false
}
