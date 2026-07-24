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
	cmd, ctx, cancel := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "push", "origin", "main")
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
	// Fake dolt: background a pipe-holding grandchild, then hang forever.
	script := "#!/bin/sh\nsleep 60 &\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(cliExecTimeoutEnv, "1s")

	cmd, ctx, cancel := prepareDoltCLITransferCommand(context.Background(), binDir, nil, false, "push", "gcs", "main")
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
