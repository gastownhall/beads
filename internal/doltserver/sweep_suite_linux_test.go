//go:build linux

package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Exercise the public sweep and real signal path in the order parallel suites
// can finish: clean B first, leaking A second. Linux retains a deleted cwd's
// path in /proc, so this does not depend on a Dolt installation or lsof.
func TestSweepSuiteTestServersPreservesForeignLeak(t *testing.T) {
	t.Setenv(AllowLeakEnv, "")
	rootA, rootB := t.TempDir(), t.TempDir()
	cwd := filepath.Join(rootA, "leaked-server")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	// The selector recognizes dolt/sql-server in the command line. A shell
	// blocked on stdin is a deterministic stand-in with no child processes.
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if got, deleted := readProcCwd(cmd.Process.Pid); !deleted || got == "" {
		t.Fatalf("stand-in cwd = %q, deleted = %v; want a deleted cwd", got, deleted)
	}

	for _, roots := range [][]string{nil, {rootB}} {
		var swept []SweptServer
		var code int
		out := captureStderr(t, func() {
			swept = SweepSuiteTestServers(roots...)
			code = ApplyLeakPolicy("clean B", 0, swept)
		})
		if len(swept) != 0 || code != 0 || out != "" {
			t.Fatalf("clean sweep %v: swept=%v code=%d stderr=%q", roots, swept, code, out)
		}
		select {
		case <-exited:
			t.Fatal("clean suite consumed the leaking suite's evidence")
		default:
		}
	}

	var swept []SweptServer
	var code int
	captureStderr(t, func() {
		swept = SweepSuiteTestServers(rootA)
		code = ApplyLeakPolicy("leaking A", 0, swept)
	})
	if len(swept) != 1 || swept[0].PID != cmd.Process.Pid || code != 1 {
		t.Fatalf("owning suite: swept=%v code=%d; want its server and failure", swept, code)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("owning suite left its leaked server running")
	}
}

// waitForExecArgv waits until pid's /proc/<pid>/cmdline is non-empty. Go's
// cmd.Start returns once exec has closed its close-on-exec pipe, which the
// kernel does before it sets up the new image's argument area, so a read
// right after Start can briefly see an empty command line.
func waitForExecArgv(t *testing.T, pid int) {
	t.Helper()
	path := filepath.Join("/proc", strconv.Itoa(pid), "cmdline")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d never exposed its argv in %s", pid, path)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSweepSuiteTestServersSparesNonDoltArgv0 is the #6186 hazard: a process
// that is not dolt but whose command line mentions both "dolt" and
// "sql-server" (here a shell, as a `tail -f dolt-sql-server.log` would be)
// sits inside a suite-owned root. The cwd gate admits it, so only the
// identity check stands between it and SIGTERM/SIGKILL.
func TestSweepSuiteTestServersSparesNonDoltArgv0(t *testing.T) {
	t.Setenv(AllowLeakEnv, "")
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	pid := cmd.Process.Pid
	waitForExecArgv(t, pid)
	if cwd, _ := readProcCwd(pid); cwd == "" {
		t.Fatalf("stand-in %d has no readable cwd; the sweep could not have reached it", pid)
	}

	var swept []SweptServer
	captureStderr(t, func() { swept = SweepSuiteTestServers(root) })
	for _, s := range swept {
		if s.PID == pid {
			t.Errorf("sweep reported non-dolt process %d: %v", pid, swept)
		}
	}
	if isDoltServerProcess(pid) {
		t.Errorf("isDoltServerProcess(%d) = true for argv[0] /bin/sh", pid)
	}
	select {
	case <-exited:
		t.Fatal("sweep signaled a non-dolt process")
	case <-time.After(500 * time.Millisecond):
	}
}
