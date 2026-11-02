//go:build unix

package execgrace

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The teardown tests re-exec this test binary (os.Args[0]) as the child, in
// the two shapes a hung dolt CLI takes: one that traps SIGTERM and finishes
// its teardown, and one that ignores SIGTERM while a grandchild (the
// stand-in for the spawned git/ssh transport, GH#7989) holds the inherited
// pipes. helperEnv gates the re-exec so it never runs the suite.

const (
	helperEnv     = "EXECGRACE_TEST_HELPER"
	helperModeTrap = "trap" // traps SIGTERM, reports it, exits cleanly
	helperModeHold = "hold" // ignores SIGTERM; a grandchild holds the pipes
)

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	switch mode {
	case helperModeTrap:
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		os.Stdout.WriteString("running\n")
		<-sig
		os.Stdout.WriteString("sigterm\n")
		os.Exit(0)
	case helperModeHold:
		signal.Ignore(syscall.SIGTERM)
		os.Stdout.WriteString("running\n")
		// A grandchild that inherits our stdout, standing in for the
		// transport children a real dolt CLI owns.
		grandchild := exec.Command("sleep", "60")
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		_ = grandchild.Start()
	}
	select {} // wait for the parent's teardown to reach us
}

// startHelperChild launches the helper in mode, configured like a bd-owned
// child, and returns its command plus a reader for the child's stdout pipe
// (closed after Start so the parent holds the only read end).
func startHelperChild(t *testing.T, mode string, ctx context.Context) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("make helper pipe: %v", err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cmd.Stdout = pw
	cmd.Stderr = pw
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper child: %v", err)
	}
	pw.Close()
	t.Cleanup(func() {
		_ = pr.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return cmd, bufio.NewReader(pr)
}

// TestConfigureSIGTERMReachesChild pins the graceful half of the contract:
// when the context expires, the immediate child receives SIGTERM — visible
// because it completes its teardown and reports the signal — where
// CommandContext's default cancel would have killed it outright.
func TestConfigureSIGTERMReachesChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd, out := startHelperChild(t, helperModeTrap, ctx)

	line, err := out.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "running" {
		t.Fatalf("helper never started (line %q: %v)", line, err)
	}

	line, err = out.ReadString('\n')
	if err != nil {
		t.Fatalf("helper never reported SIGTERM (it was killed hard instead?): %v", err)
	}
	if strings.TrimSpace(line) != "sigterm" {
		t.Fatalf("helper reported %q, want %q", line, "sigterm")
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err == nil {
			t.Fatal("Wait returned nil; a context that expired before the child completed must report its error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Wait = %v, want the context error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait blocked after the child exited cleanly on SIGTERM")
	}
}

// TestConfigureReapsChildThatIgnoresSIGTERM pins the bounded half: a child
// that ignores SIGTERM while a grandchild holds the pipes is still reaped —
// Wait returns with the context error once the WaitDelay expires and the
// process is killed — instead of blocking until the child's natural exit.
func TestConfigureReapsChildThatIgnoresSIGTERM(t *testing.T) {
	// Cannot be parallel: shrinks GracePeriod.
	prev := GracePeriod
	GracePeriod = 300 * time.Millisecond
	t.Cleanup(func() { GracePeriod = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd, out := startHelperChild(t, helperModeHold, ctx)

	line, err := out.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "running" {
		t.Fatalf("helper never started (line %q: %v)", line, err)
	}

	start := time.Now()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Wait returned nil; the expired context must be reported")
		}
		// The forced kill after the grace window surfaces as the kill's own
		// exit error; a child that exited on SIGTERM (the trap test) reports
		// the context error instead. Either way the reap is bounded.
		var exitErr *exec.ExitError
		if !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &exitErr) {
			t.Fatalf("Wait = %v, want the context error or the forced kill's exit error", err)
		}
		if elapsed < 300*time.Millisecond {
			t.Fatalf("Wait returned in %s, before the grace window expired", elapsed)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("Wait blocked %s; the pipe-holding child was not reaped boundedly", elapsed)
		}
		if cmd.ProcessState == nil {
			t.Fatal("Wait returned without reaping the child process")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait blocked forever; WaitDelay did not bound the teardown")
	}
}