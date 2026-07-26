//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type sweepTestRunner struct {
	run func() int
}

func (r sweepTestRunner) Run() int {
	return r.run()
}

func TestRunTestsAndSweepReapsOrphanedServer(t *testing.T) {
	oldRoot := testTempRoot
	testTempRoot = t.TempDir()
	t.Cleanup(func() { testTempRoot = oldRoot })

	serverDir := filepath.Join(testTempRoot, "orphan", ".beads", "dolt")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatalf("mkdir server dir: %v", err)
	}
	script := filepath.Join(testTempRoot, "dolt")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatalf("write fake dolt: %v", err)
	}

	var cmd *exec.Cmd
	runner := sweepTestRunner{run: func() int {
		cmd = exec.Command(script, "sql-server")
		cmd.Dir = serverDir
		if err := cmd.Start(); err != nil {
			t.Fatalf("start fake dolt sql-server: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return 0
	}}

	if code := runTestsAndSweep(runner); code != 0 {
		t.Fatalf("runTestsAndSweep() = %d, want 0", code)
	}
	if cmd == nil || cmd.Process == nil {
		t.Fatal("runner did not start fake dolt sql-server")
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("fake dolt sql-server exited unexpectedly: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runTestsAndSweep left fake dolt sql-server running")
	}
}
