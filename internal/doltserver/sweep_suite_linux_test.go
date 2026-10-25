//go:build linux

package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
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
