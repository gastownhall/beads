package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
)

// TestCLIExecTimeoutDuration verifies BEADS_CLI_TRANSFER_TIMEOUT parsing:
// valid durations and bare seconds are honored, unset/invalid/non-positive
// values fall back to the compiled-in default.
func TestCLIExecTimeoutDuration(t *testing.T) {
	t.Run("duration string", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "20m")
		if got := cliExecTimeoutDuration(); got != 20*time.Minute {
			t.Fatalf("cliExecTimeoutDuration() = %v, want 20m", got)
		}
	})
	t.Run("bare seconds", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "90")
		if got := cliExecTimeoutDuration(); got != 90*time.Second {
			t.Fatalf("cliExecTimeoutDuration() = %v, want 90s", got)
		}
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "soon")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
	t.Run("non-positive falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "-5s")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
}

// TestPrepareDoltCLITransferCommandSetsDeadlineAndWaitDelay pins the two
// guards against indefinite transfer hangs: the returned context carries the
// configured deadline, and the command has a non-zero WaitDelay so Wait cannot
// block forever on inherited pipes after a context kill.
func TestPrepareDoltCLITransferCommandSetsDeadlineAndWaitDelay(t *testing.T) {
	withNoDoltOnPath(t)
	cmd, ctx, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "push", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if cmd.WaitDelay == 0 {
		t.Fatal("cmd.WaitDelay = 0; a grandchild holding the output pipes would block Wait forever after a context kill")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("returned context has no deadline; transfer would be unbounded")
	}
	if remaining := time.Until(deadline); remaining > cliExecTimeout {
		t.Fatalf("deadline %v from now exceeds cliExecTimeout %v", remaining, cliExecTimeout)
	}
}

// TestCLITransferTimeoutUnblocksDespiteGrandchildPipes reproduces the wy-5f2u7
// hang shape: the direct child is killed at the context deadline, but a
// grandchild (stand-in for a cloud credential helper) still holds the
// inherited stdout/stderr pipes. Without WaitDelay, CombinedOutput blocks
// until the grandchild exits (60s here); with it, the call returns promptly.
func TestCLITransferTimeoutUnblocksDespiteGrandchildPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	binDir := t.TempDir()
	// Fake dolt: report a version at the floor (the transfer checks it
	// first), else background a pipe-holding grandchild, then hang forever.
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + doltversion.RequiredMin.String() + "'; exit 0; fi\nsleep 60 &\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(cliExecTimeoutEnv, "1s")

	cmd, ctx, cancel, err := prepareDoltCLITransferCommand(context.Background(), binDir, nil, false, "push", "gcs", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	cmd.WaitDelay = 500 * time.Millisecond // keep the test fast; default is cliExecWaitDelay

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("CombinedOutput succeeded; want kill after timeout (out=%q)", out)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("CombinedOutput blocked %v despite 1s timeout + WaitDelay; pipe-holding grandchild kept Wait stuck", elapsed)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
	msg := cliTransferError("dolt push", "gcs", ctx, out, err).Error()
	if !strings.Contains(msg, "timed out after 1s") || !strings.Contains(msg, cliExecTimeoutEnv) {
		t.Fatalf("timeout error not actionable: %q", msg)
	}
}

// TestCLITransferErrorPlainFailure pins that a non-timeout failure keeps the
// ordinary error shape and does not claim a timeout.
func TestCLITransferErrorPlainFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	msg := cliTransferError("dolt push", "gcs", ctx, []byte("remote not found"), errors.New("exit status 1")).Error()
	if strings.Contains(msg, "timed out") {
		t.Fatalf("non-timeout failure misreported as timeout: %q", msg)
	}
	if !strings.Contains(msg, "dolt push failed: remote not found") {
		t.Fatalf("unexpected error shape: %q", msg)
	}
}

// withNoDoltOnPath points PATH at an empty directory for t, so a test of the
// command prepareDoltCLITransferCommand builds does not depend on the host's
// dolt, whose version the function checks first (a dolt not on PATH is not
// checked).
func withNoDoltOnPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// TestPrepareDoltCLITransferCommandRefusesDoltBelowRequiredMin pins the
// binary floor on the CLI transfer route (git-protocol, credentialed,
// cloud-auth and local remotes): a dolt on PATH whose `dolt version` reports
// a release below doltversion.RequiredMin is refused with the floor's error
// before push, pull or fetch runs, like every other `dolt` bd runs (when
// bd's own server holds the database, dolt hands the transfer to that
// server; otherwise the binary's own engine runs it). A dolt at the minimum
// is let through.
func TestPrepareDoltCLITransferCommandRefusesDoltBelowRequiredMin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dolt stub is a POSIX shell script")
	}
	calls := filepath.Join(t.TempDir(), "calls")
	stubOnPath := func(t *testing.T, version string) {
		t.Helper()
		binDir := t.TempDir()
		stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + version + "'; exit 0; fi\necho \"$@\" >> '" + calls + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(stub), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	for _, args := range [][]string{{"push", "origin", "main"}, {"pull", "origin", "main"}, {"fetch", "origin"}} {
		t.Run(args[0], func(t *testing.T) {
			stubOnPath(t, "2.5.1")
			cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, args...)
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				if cancel != nil {
					cancel()
				}
				t.Fatalf("prepareDoltCLITransferCommand(%v) = %v, want ErrBelowRequiredMin", args, err)
			}
			if cmd != nil {
				t.Errorf("got a command %v alongside the refusal", cmd.Args)
			}
			for _, want := range []string{"dolt " + args[0], "2.5.1", "requires dolt " + doltversion.RequiredMin.String(), "Upgrade dolt"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal = %q, want it to contain %q", err, want)
				}
			}
		})
	}
	if got, err := os.ReadFile(calls); err == nil {
		t.Fatalf("the dolt stub ran %q after a refusal, want only `dolt version`", got)
	}

	t.Run("at the minimum", func(t *testing.T) {
		stubOnPath(t, doltversion.RequiredMin.String())
		cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "pull", "origin", "main")
		if err != nil {
			t.Fatalf("prepareDoltCLITransferCommand = %v, want nil for dolt %s", err, doltversion.RequiredMin)
		}
		defer cancel()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("transfer failed: %v: %s", err, out)
		}
		if got, err := os.ReadFile(calls); err != nil || !strings.Contains(string(got), "pull origin main") {
			t.Fatalf("stub calls = %q (%v), want the pull to have run", got, err)
		}
	})
}

// TestCLIRouteRefusesDoltBelowRequiredMinBeforeRunningIt pins where the CLI
// route checks the dolt on PATH: as soon as it has chosen the CLI, so the
// refusal is the first thing an old binary meets. The push routes refuse it
// before prePushFSCK's `dolt fsck` (which would run here: the CLI directory
// has a .dolt/noms), and ensureMatchingCLIRemote before `dolt remote` lists
// or edits the CLI remote.
func TestCLIRouteRefusesDoltBelowRequiredMinBeforeRunningIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dolt stub is a POSIX shell script")
	}
	newStore := func(t *testing.T) *DoltStore {
		t.Helper()
		s := &DoltStore{dbPath: t.TempDir(), database: "beads", branch: "main"}
		if err := os.MkdirAll(filepath.Join(s.CLIDir(), ".dolt", "noms"), 0o750); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		name string
		run  func(*DoltStore) error
	}{
		{"push", func(s *DoltStore) error { return s.doltCLIPush(context.Background(), "origin", false, nil) }},
		{"push to peer", func(s *DoltStore) error {
			return s.doltCLIPushRefToPeer(context.Background(), "peer", "main", nil)
		}},
		{"CLI remote", func(s *DoltStore) error { return s.ensureMatchingCLIRemote("origin", "file:///nonexistent/remote", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubDoltVersionOnPath(t, "2.5.1")
			err := tc.run(newStore(t))
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("%s = %v, want ErrBelowRequiredMin", tc.name, err)
			}
			if got, err := os.ReadFile(calls); err == nil {
				t.Fatalf("the dolt stub ran %q before the refusal, want only `dolt version`", got)
			}
		})
	}
}

// stubDoltVersionOnPath puts a fake dolt first on PATH for t: it answers
// `dolt version` with version and records every other invocation in the
// returned calls file.
func stubDoltVersionOnPath(t *testing.T, version string) (calls string) {
	t.Helper()
	binDir := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + version + "'; exit 0; fi\necho \"$@\" >> '" + calls + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(stub), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}
