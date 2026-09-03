package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/workspacegate"
)

// resetGateTestEnv pins every env var the physical-root resolver consults so
// a developer machine's beads setup (shared-server mode, custom data dirs,
// central config) cannot change which gates these tests acquire.
func resetGateTestEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_DATA_DIR",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_PROXIED_SERVER_ROOT_PATH",
		"BEADS_SHARED_SERVER_DIR",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "no-central.json"))
}

func newGateTestWorkspace(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"dolt","database":"beads.db","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

func TestCommandNeedsExclusiveGate(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	backup := &cobra.Command{Use: "backup"}
	backupRestore := &cobra.Command{Use: "restore [path]"}
	backup.AddCommand(backupRestore)
	root.AddCommand(backup)
	// Top-level `bd restore <issue-id>` is an ISSUE restore
	// (cmd/bd/restore.go), not a database restore: it must stay SHARED.
	issueRestore := &cobra.Command{Use: "restore [issue-id]"}
	root.AddCommand(issueRestore)
	list := &cobra.Command{Use: "list"}
	root.AddCommand(list)

	cases := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"backup restore is exclusive", backupRestore, true},
		{"top-level issue restore is not", issueRestore, false},
		{"list is not", list, false},
		{"backup parent is not", backup, false},
		{"root is not", root, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandNeedsExclusiveGate(tc.cmd); got != tc.want {
				t.Errorf("commandNeedsExclusiveGate(%s) = %v, want %v", tc.cmd.Name(), got, tc.want)
			}
		})
	}
}

func TestAcquireCommandWorkspaceGatesAbsentWorkspace(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)

	list := &cobra.Command{Use: "list"}
	missing := filepath.Join(t.TempDir(), "nope", ".beads")
	if err := acquireCommandWorkspaceGates(context.Background(), list, missing); err != nil {
		t.Fatalf("absent beadsDir must be silently ungated, got %v", err)
	}
	if workspaceGateHandle != nil {
		t.Error("absent beadsDir must leave no gate handle")
	}
}

func TestAcquireCommandWorkspaceGatesBlockedByExclusiveHolder(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)

	gate, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := gate.Acquire(context.Background(), workspacegate.Exclusive,
		workspacegate.Options{Reason: "test maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Release() }()

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err == nil {
		t.Fatal("SHARED acquisition under a foreign exclusive holder must abort, got nil error")
	}
	if workspaceGateHandle != nil {
		t.Error("failed acquisition must leave no gate handle")
	}
}

func TestAcquireInitMutationGateKeepsReplacementExclusiveDuringPreflight(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")

	oldOnWait := exclusiveGateOnWait
	secondWaited := make(chan struct{}, 1)
	exclusiveGateOnWait = func(string) {
		select {
		case secondWaited <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { exclusiveGateOnWait = oldOnWait })

	firstPreflightEntered := make(chan struct{})
	allowFirstPreflight := make(chan struct{})
	type result struct {
		h   *workspacegate.MultiHandle
		err error
	}
	firstResult := make(chan result, 1)
	go func() {
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
			close(firstPreflightEntered)
			<-allowFirstPreflight
			return nil
		})
		firstResult <- result{h: h, err: err}
	}()
	<-firstPreflightEntered

	secondPreflightEntered := make(chan struct{})
	secondResult := make(chan result, 1)
	go func() {
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
			close(secondPreflightEntered)
			return nil
		})
		secondResult <- result{h: h, err: err}
	}()

	select {
	case <-secondWaited:
	case <-secondPreflightEntered:
		t.Fatal("second replacement entered preflight while first held the mutation gates")
	}

	close(allowFirstPreflight)
	first := <-firstResult
	if first.err != nil {
		t.Fatalf("first init mutation gate: %v", first.err)
	}
	if err := first.h.Release(); err != nil {
		t.Fatalf("release first init mutation gate: %v", err)
	}

	<-secondPreflightEntered
	second := <-secondResult
	if second.err != nil {
		t.Fatalf("second init mutation gate: %v", second.err)
	}
	if err := second.h.Release(); err != nil {
		t.Fatalf("release second init mutation gate: %v", err)
	}
}

func TestAcquireInitMutationGateReleasesOnPreflightError(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	refusal := errors.New("destroy token rejected")

	_, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
		return refusal
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("init mutation gate error = %v, want preflight refusal", err)
	}

	h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, nil)
	if err != nil {
		t.Fatalf("init mutation gate remained held after refusal: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("release init mutation gate: %v", err)
	}
}

func TestReleaseWorkspaceGatesIdempotent(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if workspaceGateHandle == nil {
		t.Fatal("expected a held gate handle")
	}
	releaseWorkspaceGates()
	if workspaceGateHandle != nil {
		t.Error("handle must be cleared on release")
	}
	// Second release must be a no-op, not a panic or double-unlock.
	releaseWorkspaceGates()

	// And the gate must actually be free again: an exclusive acquisition
	// succeeds after release.
	gate, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := gate.Acquire(context.Background(), workspacegate.Exclusive, workspacegate.Options{})
	if err != nil {
		t.Fatalf("gate still held after releaseWorkspaceGates: %v", err)
	}
	_ = h.Release()
}

// The cross-wiring guarantee: a chokepoint SHARED hold (a normal command
// mid-flight) excludes acquireMigrateGates' EXCLUSIVE acquisition on the
// same workspace. Also exercises the nil-rootCtx path inside
// acquireMigrateGates (tests have no process signal context), which used to
// panic before the nil-context normalization.
func TestChokepointSharedExcludesMigrateExclusive(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)

	// rootCtx is a package global that production sets via
	// setupGracefulShutdown() in PersistentPreRunE and cancels via
	// rootCancel() in PersistentPostRunE WITHOUT resetting the var to nil —
	// harmless in production (the process exits), but any earlier in-process
	// test that exercises the full command path (Execute()) leaves rootCtx
	// pointing at an already-canceled context for whatever test runs next in
	// the same binary. acquireMigrateGates now threads rootCtx through to
	// acquireExclusiveWorkspaceGates, so this test is sensitive to that
	// leak: pin it to nil (the documented "no process signal context yet"
	// case this test exercises) regardless of what ran before it.
	oldRootCtx := rootCtx
	rootCtx = nil
	t.Cleanup(func() { rootCtx = oldRootCtx })

	oldWait := exclusiveGateWait
	exclusiveGateWait = 10 * time.Millisecond
	t.Cleanup(func() { exclusiveGateWait = oldWait })

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if workspaceGateHandle == nil {
		t.Fatal("expected a held shared gate handle")
	}

	release, err := acquireMigrateGates(beadsDir, false, "test migrate")
	if err == nil {
		release()
		t.Fatal("migrate EXCLUSIVE acquisition must fail while the chokepoint holds SHARED")
	}

	// After the shared holder releases, the migration proceeds.
	releaseWorkspaceGates()
	release, err = acquireMigrateGates(beadsDir, false, "test migrate")
	if err != nil {
		t.Fatalf("migrate acquisition after shared release: %v", err)
	}
	release()
}
