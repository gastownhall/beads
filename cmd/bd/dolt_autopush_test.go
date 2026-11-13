package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

type fakeAutoPushTarget struct {
	commit    string
	commitErr error
	push      func(context.Context) error
}

func (f *fakeAutoPushTarget) GetCurrentCommit(context.Context) (string, error) {
	return f.commit, f.commitErr
}

func (f *fakeAutoPushTarget) Push(ctx context.Context) error {
	return f.push(ctx)
}

func TestIsDoltAutoPushEnabled_ExplicitConfig(t *testing.T) {
	// Cannot be parallel: modifies global env vars and config.

	tests := []struct {
		name       string
		envVal     string // "true"/"false" = explicit config via env
		wantResult bool
	}{
		{
			name:       "explicit true → enabled",
			envVal:     "true",
			wantResult: true,
		},
		{
			name:       "explicit false → disabled",
			envVal:     "false",
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BD_DOLT_AUTO_PUSH", tt.envVal)

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			// With explicit config, store check is bypassed
			// (store is nil in this test, which would return false for auto-detection)
			got := isDoltAutoPushEnabled(context.Background())
			if got != tt.wantResult {
				t.Errorf("isDoltAutoPushEnabled() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestIsDoltAutoPushEnabled_DefaultOff(t *testing.T) {
	// Default (no explicit config) must return false — auto-push is opt-in only.
	os.Unsetenv("BD_DOLT_AUTO_PUSH")
	t.Cleanup(func() { os.Unsetenv("BD_DOLT_AUTO_PUSH") })

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	got := isDoltAutoPushEnabled(context.Background())
	if got != false {
		t.Errorf("isDoltAutoPushEnabled() default = %v, want false", got)
	}
}

func TestMaybeAutoPush_NilStore(t *testing.T) {
	// maybeAutoPush should be a no-op when store is nil (no panic).
	os.Unsetenv("BD_DOLT_AUTO_PUSH")
	t.Cleanup(func() { os.Unsetenv("BD_DOLT_AUTO_PUSH") })

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// Should not panic with nil store
	maybeAutoPush(context.Background())
}

func TestAutoPush_SkippedForReadOnlyCommands(t *testing.T) {
	// Read-only commands should not trigger auto-push (GH#2191).
	readOnly := []string{"list", "ready", "show", "stats", "blocked", "search", "query", "graph"}
	for _, cmd := range readOnly {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true", cmd)
		}
	}

	writeCmds := []string{"create", "update", "close", "import"}
	for _, cmd := range writeCmds {
		if isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false", cmd)
		}
	}
}

func TestAutoPushTimeoutConstants(t *testing.T) {
	// Verify timeout defaults are reasonable (GH#3370).
	if autoPushTimeout < 10*time.Second || autoPushTimeout > 120*time.Second {
		t.Errorf("autoPushTimeout = %s, want 10s-120s range", autoPushTimeout)
	}
}

func TestPushWithContextReturnsPushResult(t *testing.T) {
	target := &fakeAutoPushTarget{
		push: func(context.Context) error {
			return nil
		},
	}

	if err := pushWithContext(context.Background(), target); err != nil {
		t.Fatalf("pushWithContext() = %v, want nil", err)
	}
}

func TestPushWithContextReturnsPushError(t *testing.T) {
	wantErr := errors.New("push failed")
	target := &fakeAutoPushTarget{
		push: func(context.Context) error {
			return wantErr
		},
	}

	err := pushWithContext(context.Background(), target)
	if !errors.Is(err, wantErr) {
		t.Fatalf("pushWithContext() = %v, want %v", err, wantErr)
	}
}

func TestPushWithContextBoundsIgnoredContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	target := &fakeAutoPushTarget{
		push: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := pushWithContext(ctx, target)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pushWithContext() = %v, want deadline exceeded", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("pushWithContext returned before Push started")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("pushWithContext took %s, want under 500ms", elapsed)
	}
}

func TestMaybeAutoPush_CancelledContext(t *testing.T) {
	// maybeAutoPush should handle cancelled context gracefully (GH#3370).
	t.Setenv("BD_DOLT_AUTO_PUSH", "true")

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	maybeAutoPush(ctx)
}

func TestMaybeAutoPush_DisabledByConfig(t *testing.T) {
	// When explicitly disabled, maybeAutoPush should be a no-op.
	t.Setenv("BD_DOLT_AUTO_PUSH", "false")

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// Should not panic or attempt push
	maybeAutoPush(context.Background())
}

func TestLoadSavePushState(t *testing.T) {

	// Create a temp .beads dir with metadata.json so FindBeadsDir works
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	// No file yet → nil, nil
	ps, err := loadPushState()
	if err != nil {
		t.Fatalf("loadPushState (no file): %v", err)
	}
	if ps != nil {
		t.Fatalf("loadPushState (no file): got %+v, want nil", ps)
	}

	// Save and reload
	want := &pushState{LastPush: "2026-03-09T12:00:00Z", LastCommit: "abc123"}
	if err := savePushState(want); err != nil {
		t.Fatalf("savePushState: %v", err)
	}
	got, err := loadPushState()
	if err != nil {
		t.Fatalf("loadPushState: %v", err)
	}
	if got == nil || got.LastPush != want.LastPush || got.LastCommit != want.LastCommit {
		t.Errorf("loadPushState = %+v, want %+v", got, want)
	}
}

func TestLoadPushState_CorruptJSON(t *testing.T) {

	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	// Write garbage
	if err := os.WriteFile(filepath.Join(beadsDir, "push-state.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadPushState()
	if err == nil {
		t.Error("loadPushState with corrupt JSON: expected error, got nil")
	}
}

// fakeAutoPushStore is a minimal DoltStorage stand-in for the no-git-ops
// auto-push pin (GH#3463). It embeds a nil DoltStorage for the identity
// assertion in maybeAutoPush (UnwrapStore cannot peel it and the fake is not
// a LifecycleManager, so IsClosed is never consulted) and overrides only the
// methods the auto-push path touches. HasRemote reports an "origin" remote
// although the current code never asks: the pre-#3446 auto-detect did ask,
// so restoring that behavior makes this test observe a real push instead of
// panicking on the nil embed.
type fakeAutoPushStore struct {
	storage.DoltStorage
	commit    string
	pushCalls int
}

func (f *fakeAutoPushStore) GetCurrentCommit(context.Context) (string, error) { return f.commit, nil }
func (f *fakeAutoPushStore) HasRemote(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (f *fakeAutoPushStore) Push(context.Context) error {
	f.pushCalls++
	return nil
}

// setupNoGitOpsAutoPushWorkspace builds a temp workspace whose
// .beads/config.yaml holds configYAML, points BEADS_DIR and the user-level
// config roots at temp locations so nothing leaks in or out, (re)initializes
// config, and installs a fake store. It returns the .beads dir and the
// installed store.
func setupNoGitOpsAutoPushWorkspace(t *testing.T, configYAML string) (string, *fakeAutoPushStore) {
	t.Helper()

	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"metadata.json": `{}`,
		"config.yaml":   configYAML,
	} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)

	// Fence out user-level config so a developer's own ~/.config/bd/config.yaml
	// cannot leak dolt.auto-push or dolt.local-only into the subtest.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// GetValueSource counts set-but-empty env vars as explicit, so unset the
	// keys rather than setting them empty. t.Setenv first registers the
	// restore of the original values as a subtest cleanup.
	for _, key := range []string{"BD_DOLT_AUTO_PUSH", "BD_DOLT_LOCAL_ONLY"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !config.GetBool("no-git-ops") {
		t.Fatal("precondition: no-git-ops must be read from the workspace config.yaml")
	}

	fake := &fakeAutoPushStore{commit: "deadbeef"}
	prev := getStore()
	setStore(fake)
	t.Cleanup(func() { setStore(prev) })

	return beadsDir, fake
}

// TestMaybeAutoPush_NoGitOpsDoesNotPush pins the GH#3463 contract that holds
// since auto-push became explicit opt-in (#3446): no-git-ops hides bd's own
// git activity (the bd prime close protocol), it does not stop Dolt remote
// sync. An explicit dolt.auto-push opt-in still pushes, while
// dolt.local-only — the switch that does govern Dolt networking — never
// does. The fake store reports an "origin" remote, so if the old
// push-whenever-origin-exists auto-detect ever comes back, this test fails
// instead of silently passing.
func TestMaybeAutoPush_NoGitOpsDoesNotPush(t *testing.T) {
	// Cannot be parallel: mutates env vars, config, and the store global.

	t.Run("auto_push_unset", func(t *testing.T) {
		// The reporter's setup (GH#3463): no-git-ops set, dolt.auto-push
		// never opted in. Nothing may touch the remote or the push state.
		beadsDir, fake := setupNoGitOpsAutoPushWorkspace(t, "no-git-ops: true\n")
		if src := config.GetValueSource("dolt.auto-push"); src != config.SourceDefault {
			t.Fatalf("precondition: dolt.auto-push source = %v, want %s", src, config.SourceDefault)
		}

		maybeAutoPush(context.Background())

		if fake.pushCalls != 0 {
			t.Errorf("Push calls = %d, want 0 (no-git-ops with dolt.auto-push unset must not push; GH#3463)", fake.pushCalls)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "push-state.json")); err == nil {
			t.Error("push-state.json was created although auto-push never ran")
		}
	})

	t.Run("explicit_auto_push", func(t *testing.T) {
		// no-git-ops does not override an explicit dolt.auto-push opt-in:
		// a stealth workspace that deliberately configured replication
		// keeps it (GH#3463 expected behavior, second bullet).
		beadsDir, fake := setupNoGitOpsAutoPushWorkspace(t, "no-git-ops: true\ndolt:\n  auto-push: true\n")
		if !config.GetBool("dolt.auto-push") {
			t.Fatal("precondition: dolt.auto-push must be read from the workspace config.yaml")
		}

		maybeAutoPush(context.Background())

		if fake.pushCalls != 1 {
			t.Fatalf("Push calls = %d, want 1 (no-git-ops must not override an explicit dolt.auto-push opt-in)", fake.pushCalls)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "push-state.json")); err != nil {
			t.Fatalf("push-state.json must exist after a successful auto-push: %v", err)
		}

		// The debounce window must hold for an immediate second tick.
		maybeAutoPush(context.Background())
		if fake.pushCalls != 1 {
			t.Errorf("Push calls after an immediate second tick = %d, want 1 (debounce interval still open)", fake.pushCalls)
		}
	})

	t.Run("local_only_wins", func(t *testing.T) {
		// dolt.local-only is the switch that governs Dolt networking: even an
		// explicit auto-push opt-in must not push while it is set.
		_, fake := setupNoGitOpsAutoPushWorkspace(t, "no-git-ops: true\ndolt:\n  auto-push: true\n  local-only: true\n")
		if !config.GetBool("dolt.local-only") {
			t.Fatal("precondition: dolt.local-only must be read from the workspace config.yaml")
		}

		maybeAutoPush(context.Background())

		if fake.pushCalls != 0 {
			t.Errorf("Push calls = %d, want 0 (dolt.local-only suppresses auto-push even when opted in)", fake.pushCalls)
		}
	})
}

// TestPushedCommitForState pins the push-state hash recorded after a successful
// auto-push. Push commits pending changes before pushing (GH#5433), so HEAD can
// move during the push call and the pre-push hash is no longer what was pushed.
func TestPushedCommitForState(t *testing.T) {
	ctx := context.Background()

	t.Run("records the post-push commit", func(t *testing.T) {
		target := &fakeAutoPushTarget{commit: "after"}
		if got := pushedCommitForState(ctx, target, "before"); got != "after" {
			t.Fatalf("pushedCommitForState() = %q, want %q (the commit the guard created, not its parent)", got, "after")
		}
	})

	// The push already succeeded here, so a failed re-read must not drop
	// LastCommit -- that would lose change detection entirely. The fake still
	// returns a commit alongside the error, so this can only pass if the error
	// branch is actually taken.
	t.Run("falls back to the pre-push commit when the re-read fails", func(t *testing.T) {
		target := &fakeAutoPushTarget{commit: "after", commitErr: errors.New("engine closed")}
		if got := pushedCommitForState(ctx, target, "before"); got != "before" {
			t.Fatalf("pushedCommitForState() = %q, want %q (pre-push fallback)", got, "before")
		}
	})
}
