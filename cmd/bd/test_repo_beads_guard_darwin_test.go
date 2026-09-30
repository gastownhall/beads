//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

type sweepTestRunner struct {
	run func() int
}

func (r sweepTestRunner) Run() int {
	return r.run()
}

// TestRunTestsAndSweepReapsOrphanedServer pins both halves of the leak
// contract on a suite whose tests all pass but which leaves a detached
// `dolt sql-server` behind: the sweep always reaps it, and the reaped orphan
// now FAILS the run (exit 1) unless doltserver.AllowLeakEnv is set to "1".
//
// The failure is the point (wy-j2zc8q). Until it landed, a swept leak was a
// stderr line on an exit-0 run, so all three previous fixes for this leak
// (wy-9byjk, wy-5ce39p, and this one) rode a green CI for weeks and the
// regression was rediscovered from a dev box's process table instead.
func TestRunTestsAndSweepReapsOrphanedServer(t *testing.T) {
	cases := []struct {
		name     string
		allowEnv string
		want     int
	}{
		{name: "a swept leak fails an otherwise passing suite", allowEnv: "", want: 1},
		{name: "the opt-out downgrades the leak to a warning", allowEnv: "1", want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both cases must reap; only the exit code differs.
			t.Setenv(doltserver.AllowLeakEnv, tc.allowEnv)

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

			if code := runTestsAndSweep(runner); code != tc.want {
				t.Fatalf("runTestsAndSweep() = %d, want %d", code, tc.want)
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
		})
	}
}
