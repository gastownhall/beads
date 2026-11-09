//go:build darwin

package main

import (
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

			var cmd *exec.Cmd
			runner := sweepTestRunner{run: func() int {
				// The stand-in must satisfy the sweep's tightened identity
				// gate (#6186): argv[0] basename "dolt" plus an argument
				// exactly "sql-server". Executing /bin/bash directly — not
				// through a shebang script, whose argv[0] the kernel
				// replaces with the interpreter's path — and overriding
				// Args[0] keeps that shape on the ps command column the
				// darwin sweep tokenizes; /bin/bash avoids macOS's /bin/sh
				// trampoline. The first argument after -c is the shell's $0,
				// which is why "sql-server" rides as a later argument.
				cmd = exec.Command("/bin/bash", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done", "sql-server")
				cmd.Args[0] = "dolt"
				cmd.Dir = serverDir
				if err := cmd.Start(); err != nil {
					t.Fatalf("start fake dolt sql-server: %v", err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill() })

				// Verify the stand-in is observable the way the sweep will
				// see it, so a broken fixture cannot make the test vacuous.
				// This mirrors the production revalidation probe: ps's
				// command column, tokenized, must present argv[0] "dolt" and
				// an exact "sql-server" argument.
				out, err := exec.Command("ps", "-p", strconv.Itoa(cmd.Process.Pid), "-o", "command=").Output()
				if err != nil {
					t.Fatalf("ps -p %d: %v", cmd.Process.Pid, err)
				}
				fields := strings.Fields(string(out))
				if len(fields) < 2 || fields[0] != "dolt" || !slices.Contains(fields[1:], "sql-server") {
					t.Fatalf("darwin ps reports the stand-in as %q; the sweep's identity gate would not recognize it", out)
				}
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
