package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
)

// snapshotRootContext saves and restores BOTH representations of the root
// context. setRootContext writes the raw globals and cmdCtx, so restoring
// only the globals would leak half the state into the next test — the exact
// failure mode these tests exist to prevent.
func snapshotRootContext(t *testing.T) {
	t.Helper()
	oldCtx, oldCancel := rootCtx, rootCancel
	oldReadonly := readonlyMode
	oldUseGlobals := testModeUseGlobals
	var oldCmdCtx context.Context
	var oldCmdCancel context.CancelFunc
	if cmdCtx != nil {
		oldCmdCtx, oldCmdCancel = cmdCtx.RootCtx, cmdCtx.RootCancel
	}
	t.Cleanup(func() {
		rootCtx, rootCancel = oldCtx, oldCancel
		readonlyMode = oldReadonly
		testModeUseGlobals = oldUseGlobals
		if cmdCtx != nil {
			cmdCtx.RootCtx, cmdCtx.RootCancel = oldCmdCtx, oldCmdCancel
		}
	})
}

// PersistentPostRunE cancels the signal context on the way out. It must also
// drop the globals: a cancelled context left in rootCtx is invisible to the
// next Execute() in the same process and makes every context-aware command
// refuse work that nobody cancelled.
//
// Regression: #5093 threaded rootCtx into acquireMigrateGates, which turned
// this latent leak into eleven hard failures in cmd/bd — every earlier test
// that ran a full command path left migrate's workspace-gate acquisition
// failing with "context canceled" at 0.00s.
func TestPersistentPostRunE_ClearsCancelledRootContext(t *testing.T) {
	// strictReadonly short-circuits the post-run maintenance block, so these
	// exercise the context lifecycle without needing a live store.
	run := func(t *testing.T, useGlobals bool) {
		t.Helper()
		snapshotRootContext(t)
		testModeUseGlobals = useGlobals
		readonlyMode = true

		ctx, cancel := context.WithCancel(context.Background())
		setRootContext(ctx, cancel)

		if err := rootCmd.PersistentPostRunE(&cobra.Command{Use: "list"}, nil); err != nil {
			t.Fatalf("PersistentPostRunE: %v", err)
		}

		if err := ctx.Err(); !errors.Is(err, context.Canceled) {
			t.Errorf("post-run must cancel the context it was handed, got %v", err)
		}
		if rootCtx != nil {
			t.Errorf("rootCtx must be cleared after post-run, got %v (err=%v)", rootCtx, rootCtx.Err())
		}
		if rootCancel != nil {
			t.Error("rootCancel must be cleared after post-run")
		}
		if cmdCtx != nil && cmdCtx.RootCtx != nil {
			t.Errorf("cmdCtx.RootCtx must be cleared after post-run, got %v", cmdCtx.RootCtx)
		}
		if err := getRootContext().Err(); err != nil {
			t.Errorf("getRootContext() must hand back a live context after post-run, got %v", err)
		}
	}

	// The package TestMain pins testModeUseGlobals, so the cmdCtx branch of
	// getRootContext() is only reached if this test unpins it. Production
	// runs that branch, so it is the one that must not regress.
	t.Run("legacy globals", func(t *testing.T) { run(t, true) })
	// Whether an earlier test in the same process ran a command (and so left
	// cmdCtx initialized) depends on rules_go's shard assignment, so a skip
	// here would come and go with unrelated test additions. Initialize the
	// command context ourselves instead, and drop it again afterwards.
	t.Run("command context", func(t *testing.T) {
		if cmdCtx == nil {
			initCommandContext()
			t.Cleanup(resetCommandContext)
		}
		run(t, false)
	})
}

// The clear is deferred, not inline, so the maintenance block's error
// returns cannot strand a cancelled context in the globals either.
func TestPersistentPostRunE_ClearsOnErrorPath(t *testing.T) {
	snapshotRootContext(t)
	testModeUseGlobals = true
	readonlyMode = false // let the maintenance block run

	oldBackup, oldExport := runPostRunAutoBackup, runPostRunAutoExport
	t.Cleanup(func() { runPostRunAutoBackup, runPostRunAutoExport = oldBackup, oldExport })
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = func(context.Context, bool) error { return errors.New("boom") }

	ctx, cancel := context.WithCancel(context.Background())
	setRootContext(ctx, cancel)

	// "create" is not read-only, so post-run reaches the auto-export hook.
	// GH#6163 AC-5: the failure surfaces once through the existing exit-1
	// path — HandleError prints a single Error line and returns an
	// exitError the exit code comes from.
	var err error
	stderr := captureStderr(t, func() { err = rootCmd.PersistentPostRunE(&cobra.Command{Use: "create"}, nil) })
	if err == nil {
		t.Fatal("expected the stubbed auto-export failure to surface")
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Errorf("exitCodeFromError(err) = (%d, %v), want (1, true)", code, ok)
	}
	if n := strings.Count(stderr, "Error:"); n != 1 {
		t.Errorf("stderr = %q, want exactly one Error line, got %d", stderr, n)
	}

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("post-run must cancel the context even on the error path, got %v", err)
	}
	if rootCtx != nil || rootCancel != nil {
		t.Errorf("post-run must clear the globals on the error path, got ctx=%v cancel!=nil=%v", rootCtx, rootCancel != nil)
	}
}

// stateHashErrClosableStore adds a no-op Close to the state-hash failure
// fake: PersistentPostRunE's deferred cleanup closes whatever store is
// installed, and the nil embedded DoltStorage would panic there.
type stateHashErrClosableStore struct {
	stateHashReadErrStore
}

func (f *stateHashErrClosableStore) Close() error { return nil }

// TestPersistentPostRunE_SurfacesAutoExportFailure runs the REAL
// maybeAutoExport through PersistentPostRunE (GH#6163 AC-5): a store whose
// state hash cannot be read makes the post-run maintenance return the
// auto-export failure, reported once through the existing exit-1 path, with
// the deferred context and store cleanup still performed. At the pre-fix
// base maybeAutoExport swallowed the failure and post-run returned nil.
func TestPersistentPostRunE_SurfacesAutoExportFailure(t *testing.T) {
	snapshotRootContext(t)
	testModeUseGlobals = true
	readonlyMode = false

	initConfigForTest(t)
	config.Set("export.auto", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	store = &stateHashErrClosableStore{}
	_ = autoExportTestDir(t)

	// Isolate the maintenance net: no backup, and no tip/auto-commit state
	// leaked from whichever test ran a full command dispatch earlier.
	oldBackup := runPostRunAutoBackup
	t.Cleanup(func() { runPostRunAutoBackup = oldBackup })
	runPostRunAutoBackup = func(context.Context) {}
	oldDidWrite := commandDidWrite.Load()
	oldDidExplicitCommit := commandDidExplicitDoltCommit
	oldTipMeta := commandDidWriteTipMetadata
	oldTips := commandTipIDsShown
	t.Cleanup(func() {
		commandDidWrite.Store(oldDidWrite)
		commandDidExplicitDoltCommit = oldDidExplicitCommit
		commandDidWriteTipMetadata = oldTipMeta
		commandTipIDsShown = oldTips
	})
	commandDidWrite.Store(false)
	commandDidExplicitDoltCommit = false
	commandDidWriteTipMetadata = false
	commandTipIDsShown = make(map[string]struct{})

	ctx, cancel := context.WithCancel(context.Background())
	setRootContext(ctx, cancel)

	var err error
	stderr := captureStderr(t, func() { err = rootCmd.PersistentPostRunE(&cobra.Command{Use: "create"}, nil) })
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("PersistentPostRunE err = %v (exit %d, %v), want exit code 1", err, code, ok)
	}
	if n := strings.Count(stderr, "Error:"); n != 1 {
		t.Fatalf("stderr = %q, want exactly one Error line, got %d", stderr, n)
	}
	if !strings.Contains(stderr, "failed to read the state hash") {
		t.Fatalf("stderr = %q, want the auto-export failure reported", stderr)
	}

	// Deferred cleanup ran on the failure exit: the context this command was
	// handed is cancelled, the globals are cleared, and the store was
	// closed (and dropped, so a later command cannot reuse it).
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("post-run must cancel the context on the auto-export failure path, got %v", err)
	}
	if rootCtx != nil || rootCancel != nil {
		t.Errorf("post-run must clear the globals on the auto-export failure path, got ctx=%v cancel!=nil=%v", rootCtx, rootCancel != nil)
	}
	if store != nil {
		t.Errorf("post-run must close and drop the store on the auto-export failure path, got %v", store)
	}
}

// getRootContext must survive a command that never installed a context at
// all — the state every test in this binary starts from.
func TestGetRootContext_NilIsLive(t *testing.T) {
	snapshotRootContext(t)
	testModeUseGlobals = true
	setRootContext(nil, nil)

	if err := getRootContext().Err(); err != nil {
		t.Errorf("a nil root context must normalize to a live one, got %v", err)
	}
}
