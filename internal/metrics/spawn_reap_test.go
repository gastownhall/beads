package metrics

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStartDetachedReapsExitedChild is the GH#5900 regression: a child that
// exits while the parent stays alive must be waitpid'd, not left as a zombie.
// The helper re-execs this test binary and exits immediately; startDetached
// must reap it so `ps` never reports STAT Z under this process.
func TestStartDetachedReapsExitedChild(t *testing.T) {
	if os.Getenv("BD_TEST_FLUSHER_HELPER") == "1" {
		// Stay alive long enough for the parent to observe a live pid.
		// An instant-exit helper can vanish from `ps` before the first
		// poll, which looks identical to a successful reap and would
		// false-pass the old cmd.Process.Release() path.
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStartDetachedReapsExitedChild$") //nolint:gosec // test binary re-exec
	cmd.Env = append(os.Environ(), "BD_TEST_FLUSHER_HELPER=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	pid, err := startDetached(cmd)
	if err != nil {
		t.Fatalf("startDetached: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startDetached pid = %d, want > 0", pid)
	}

	if runtime.GOOS == "windows" {
		// Zombie STAT is a Unix process-table property. Still start the
		// child so startDetached's Wait path is exercised, then return.
		time.Sleep(400 * time.Millisecond)
		return
	}

	seenLive := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, alive := unixProcStat(pid)
		if strings.Contains(st, "Z") {
			t.Fatalf("child pid %d became a zombie (stat=%q); startDetached did not reap", pid, st)
		}
		if alive {
			seenLive = true
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if !seenLive {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		return
	}
	st, alive := unixProcStat(pid)
	if !seenLive {
		t.Fatalf("child pid %d never appeared in ps (alive=%v stat=%q)", pid, alive, st)
	}
	t.Fatalf("child pid %d still present after 3s (alive=%v stat=%q); startDetached did not reap", pid, alive, st)
}

// unixProcStat returns the `ps` STAT field for pid. alive is false when ps
// finds no such process (reaped and gone). On Windows, ps is unavailable so
// this reports not-alive after a short grace; the Wait goroutine is still
// the resource-release path under test.
func unixProcStat(pid int) (stat string, alive bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return "", false
	}
	st := strings.TrimSpace(string(out))
	if st == "" || strings.HasPrefix(st, "STAT") {
		return "", false
	}
	return st, true
}
