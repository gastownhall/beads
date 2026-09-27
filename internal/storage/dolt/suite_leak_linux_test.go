//go:build linux

package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// This process boundary exercises the real TestMain allocation and exit policy,
// not just the selector: an ordinary t.TempDir fixture must belong to the suite.
// A shell blocked on stdin supplies a real process without requiring Dolt.
func TestSuiteFixtureLeakFailsOwningSuite(t *testing.T) {
	if os.Getenv("BEADS_SUITE_LEAK_PROBE") == "1" {
		cwd := t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
		cmd.Dir = cwd
		// The parent owns the write end. Closing it also stops the stand-in
		// if this child fails before TestMain reaches the sweep.
		cmd.Stdin = os.NewFile(3, "leak-probe-stdin")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = cmd.Wait() }()
		// Intentionally omit fixture shutdown. t.TempDir removes cwd before
		// m.Run returns, so the TestMain sweep must find the deleted-cwd leak.
		return
	}

	// A foreign suite's deleted-cwd process must survive the child's sweep.
	foreign := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
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
	t.Setenv(doltserver.AllowLeakEnv, "")
	// This child is a complete suite, not one of the schema-init helpers
	// which deliberately bypass suite ownership and shutdown.
	for _, key := range helperSubprocessSentinels {
		t.Setenv(key, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSuiteFixtureLeakFailsOwningSuite$", "-test.count=1")
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
