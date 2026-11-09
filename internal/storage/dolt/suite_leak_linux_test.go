//go:build linux

package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// leakStandInCmd builds the deterministic leaked-server stand-in: a real
// /bin/sh blocked on stdin, with argv[0] "dolt" and an argument exactly
// "sql-server", the shape the sweep's tightened identity gate recognizes
// (#6186). Overriding Args[0] keeps the executable a real shell path while
// presenting the basename the gate requires.
func leakStandInCmd() *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", "read line", "sql-server")
	cmd.Args[0] = "dolt"
	return cmd
}

// assertStandInIdentity pins, straight from /proc, that the stand-in is what
// the sweep's identity gate looks for — argv[0] basename "dolt" plus an exact
// "sql-server" argument — so a broken fixture cannot produce a vacuous pass.
// This package cannot call doltserver's probe (unexported), so it reads the
// same bytes the probe does.
func assertStandInIdentity(t *testing.T, pid int) {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		t.Fatalf("read /proc/%d/cmdline: %v", pid, err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(argv) < 2 || filepath.Base(argv[0]) != "dolt" || !slices.Contains(argv[1:], "sql-server") {
		t.Fatalf("stand-in pid %d has argv %q; want argv[0] basename dolt and an exact sql-server argument", pid, argv)
	}
}

// This process boundary exercises the real TestMain allocation and exit policy,
// not just the selector: an ordinary t.TempDir fixture must belong to the suite.
// A shell blocked on stdin supplies a real process without requiring Dolt.
func TestSuiteFixtureLeakFailsOwningSuite(t *testing.T) {
	if os.Getenv("BEADS_SUITE_LEAK_PROBE") == "1" {
		cwd := t.TempDir()
		cmd := leakStandInCmd()
		cmd.Dir = cwd
		// The parent owns the write end. Closing it also stops the stand-in
		// if this child fails before TestMain reaches the sweep.
		cmd.Stdin = os.NewFile(3, "leak-probe-stdin")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		assertStandInIdentity(t, cmd.Process.Pid)
		go func() { _ = cmd.Wait() }()
		// Intentionally omit fixture shutdown. t.TempDir removes cwd before
		// m.Run returns, so the TestMain sweep must find the deleted-cwd leak.
		return
	}

	// A foreign suite's deleted-cwd process must survive the child's sweep.
	foreign := leakStandInCmd()
	foreign.Dir = t.TempDir()
	stdin, err := foreign.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = foreign.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = foreign.Process.Kill()
		<-exited
	})
	assertStandInIdentity(t, foreign.Process.Pid)
	if err := os.Remove(foreign.Dir); err != nil {
		t.Fatal(err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	t.Setenv("BEADS_SUITE_LEAK_PROBE", "1")
	t.Setenv("BEADS_TEST_SKIP", "dolt")
	// The child skips Dolt on purpose, so a lane's fail-closed switch (set
	// when this suite runs against a required server) must not turn that
	// skip into a FATAL before the child reaches its leak sweep.
	t.Setenv(testutil.EnvRequireDoltContainer, "")
	t.Setenv(doltserver.AllowLeakEnv, "")
	// This child is a complete suite, not one of the schema-init helpers
	// which deliberately bypass suite ownership and shutdown.
	for _, key := range helperSubprocessSentinels {
		t.Setenv(key, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSuiteFixtureLeakFailsOwningSuite$", "-test.count=1")
	child.Env = bazeltest.ShardFreeEnv(os.Environ())
	child.ExtraFiles = []*os.File{readEnd}
	out, err := child.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("leaking suite returned %v; want exit 1\n%s", err, out)
	}
	if !strings.Contains(string(out), "FAIL: internal/storage/dolt leaked 1 dolt sql-server") {
		t.Fatalf("suite failed without attributing its fixture leak:\n%s", out)
	}
	select {
	case <-exited:
		t.Fatal("child suite consumed the foreign suite's leak evidence")
	default:
	}
}
