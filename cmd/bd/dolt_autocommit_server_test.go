//go:build cgo

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

type fakeCommitPendingStore struct {
	storage.DoltStorage
	commitCalls  int
	pendingCalls int
	message      string
	err          error
}

func (f *fakeCommitPendingStore) Commit(_ context.Context, message string) error {
	f.commitCalls++
	f.message = message
	return f.err
}

func (f *fakeCommitPendingStore) CommitPending(_ context.Context, actor string) (bool, error) {
	f.pendingCalls++
	return true, f.err
}

func saveStorageMode(t *testing.T) {
	t.Helper()
	oldServerMode := serverMode
	oldProxiedServerMode := proxiedServerMode
	oldCmdCtx := cmdCtx
	oldStore := store
	oldUseGlobals := testModeUseGlobals
	oldDoltAutoCommit := doltAutoCommit
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	testModeUseGlobals = true
	cmdCtx = nil
	doltAutoCommit = string(doltAutoCommitOn)
	t.Cleanup(func() {
		serverMode = oldServerMode
		proxiedServerMode = oldProxiedServerMode
		cmdCtx = oldCmdCtx
		store = oldStore
		testModeUseGlobals = oldUseGlobals
		doltAutoCommit = oldDoltAutoCommit
	})
}

func TestCommitPendingIfEmbeddedSkipsServerMode(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeCommitPendingStore{}
	if err := commitPendingIfEmbedded(context.Background(), fake, "tester", doltAutoCommitParams{Command: "update"}); err != nil {
		t.Fatalf("commitPendingIfEmbedded: %v", err)
	}
	if fake.commitCalls != 0 {
		t.Fatalf("Commit calls = %d, want 0 in server mode", fake.commitCalls)
	}
}

func TestMaybeAutoCommitSkipsSQLServerMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		server      bool
		proxied     bool
		description string
	}{
		{name: "server", server: true, description: "server mode"},
		{name: "proxied", proxied: true, description: "proxied server mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveStorageMode(t)
			serverMode = tc.server
			proxiedServerMode = tc.proxied

			fake := &fakeCommitPendingStore{}
			setStore(fake)
			if err := maybeAutoCommit(context.Background(), doltAutoCommitParams{Command: "note"}); err != nil {
				t.Fatalf("maybeAutoCommit: %v", err)
			}
			if fake.commitCalls != 0 {
				t.Fatalf("Commit calls = %d, want 0 in %s", fake.commitCalls, tc.description)
			}
		})
	}
}

func TestCommitPendingIfEmbeddedCommitsWhenEnabled(t *testing.T) {
	saveStorageMode(t)
	serverMode = false
	proxiedServerMode = false

	fake := &fakeCommitPendingStore{}
	if err := commitPendingIfEmbedded(context.Background(), fake, "tester", doltAutoCommitParams{
		Command:  "update",
		IssueIDs: []string{"bd-1"},
	}); err != nil {
		t.Fatalf("commitPendingIfEmbedded: %v", err)
	}
	if fake.commitCalls != 1 {
		t.Fatalf("Commit calls = %d, want 1 in embedded mode", fake.commitCalls)
	}
	if fake.pendingCalls != 0 {
		t.Fatalf("CommitPending calls = %d, want 0", fake.pendingCalls)
	}
	if fake.message != "bd: update (auto-commit) by tester [bd-1]" {
		t.Fatalf("message = %q, want command-aware auto-commit message", fake.message)
	}
}

func TestCommitPendingIfEmbeddedHonorsBatchAndOffModes(t *testing.T) {
	for _, mode := range []doltAutoCommitMode{doltAutoCommitBatch, doltAutoCommitOff} {
		t.Run(string(mode), func(t *testing.T) {
			saveStorageMode(t)
			serverMode = false
			proxiedServerMode = false
			doltAutoCommit = string(mode)

			fake := &fakeCommitPendingStore{}
			if err := commitPendingIfEmbedded(context.Background(), fake, "tester", doltAutoCommitParams{Command: "update"}); err != nil {
				t.Fatalf("commitPendingIfEmbedded: %v", err)
			}
			if fake.commitCalls != 0 {
				t.Fatalf("Commit calls = %d, want 0 in %s mode", fake.commitCalls, mode)
			}
			if fake.pendingCalls != 0 {
				t.Fatalf("CommitPending calls = %d, want 0 in %s mode", fake.pendingCalls, mode)
			}
		})
	}
}

func TestCommitPendingIfEmbeddedPropagatesEmbeddedError(t *testing.T) {
	saveStorageMode(t)
	serverMode = false

	want := errors.New("commit failed")
	fake := &fakeCommitPendingStore{err: want}
	if err := commitPendingIfEmbedded(context.Background(), fake, "tester", doltAutoCommitParams{Command: "update"}); !errors.Is(err, want) {
		t.Fatalf("commitPendingIfEmbedded error = %v, want %v", err, want)
	}
}

func TestShouldCommitCreatePostWritesSkipsServerMode(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	if got, err := shouldCommitCreatePostWrites(&types.Issue{NoHistory: true}, true); err != nil || got {
		t.Fatal("no-history create post-writes should not issue a Dolt commit in server mode")
	}
	if got, err := shouldCommitCreatePostWrites(&types.Issue{Ephemeral: true}, true); err != nil || got {
		t.Fatal("ephemeral create post-writes should not issue a Dolt commit in server mode")
	}
	if got, err := shouldCommitCreatePostWrites(&types.Issue{}, true); err != nil || got {
		t.Fatal("server-mode create post-writes are versioned by storage and should not issue a command-level commit")
	}
	if got, err := shouldCommitCreatePostWrites(&types.Issue{}, false); err != nil || got {
		t.Fatal("create without post-writes should not issue an extra Dolt commit in server mode")
	}
}

func TestShouldCommitCreatePostWritesPreservesEmbeddedFlushWhenEnabled(t *testing.T) {
	saveStorageMode(t)
	serverMode = false

	if got, err := shouldCommitCreatePostWrites(&types.Issue{NoHistory: true}, true); err != nil || !got {
		t.Fatal("embedded create should still flush pending writes")
	}
	if got, err := shouldCommitCreatePostWrites(&types.Issue{}, false); err != nil || !got {
		t.Fatal("embedded create should keep the existing commit behavior")
	}
}

func TestShouldCommitCreatePostWritesDefaultsEmbeddedToOn(t *testing.T) {
	saveStorageMode(t)
	serverMode = false
	proxiedServerMode = false
	doltAutoCommit = ""

	if got, err := shouldCommitCreatePostWrites(&types.Issue{}, false); err != nil || !got {
		t.Fatal("unset embedded create mode should match CLI default and commit")
	}
}

func TestShouldCommitCreatePostWritesHonorsEmbeddedBatchAndOffModes(t *testing.T) {
	for _, mode := range []doltAutoCommitMode{doltAutoCommitBatch, doltAutoCommitOff} {
		t.Run(string(mode), func(t *testing.T) {
			saveStorageMode(t)
			serverMode = false
			doltAutoCommit = string(mode)

			if got, err := shouldCommitCreatePostWrites(&types.Issue{}, true); err != nil || got {
				t.Fatalf("embedded create post-writes should not commit in %s mode", mode)
			}
			if got, err := shouldCommitCreatePostWrites(&types.Issue{}, false); err != nil || got {
				t.Fatalf("embedded create without post-writes should not commit in %s mode", mode)
			}
		})
	}
}

// fakeTransactStore captures the commit message RunInTransaction receives so
// tests can assert the auto-commit policy blanked (or preserved) it.
type fakeTransactStore struct {
	storage.DoltStorage
	msgs []string
}

func (f *fakeTransactStore) RunInTransaction(_ context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	f.msgs = append(f.msgs, commitMsg)
	return fn(nil)
}

// TestIssueOpsContextDefersByModeInServerMode covers bd-4wamg: batch/off must
// defer the storage layer's per-write version commit in SQL-server mode, not
// only embedded mode. An unset mode means no Dolt default was resolved
// (non-Dolt backend) and must not defer.
func TestIssueOpsContextDefersByModeInServerMode(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		wantDeferred bool
	}{
		{mode: string(doltAutoCommitOn), wantDeferred: false},
		{mode: string(doltAutoCommitBatch), wantDeferred: true},
		{mode: string(doltAutoCommitOff), wantDeferred: true},
		{mode: "", wantDeferred: false},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			saveStorageMode(t)
			serverMode = true
			doltAutoCommit = tc.mode

			ctx, err := issueOpsContext(context.Background())
			if err != nil {
				t.Fatalf("issueOpsContext: %v", err)
			}
			if got := storageissueops.VersionCommitDeferred(ctx); got != tc.wantDeferred {
				t.Fatalf("VersionCommitDeferred = %v, want %v for mode %q in server mode", got, tc.wantDeferred, tc.mode)
			}
		})
	}
}

// TestTransactHonoringAutoCommitBlanksMessageInServerBatch: the blank message
// is what makes StageAndCommit skip the version commit, and it must happen in
// server mode too (bd-4wamg).
func TestTransactHonoringAutoCommitBlanksMessageInServerBatch(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		wantMsg string
	}{
		{mode: string(doltAutoCommitOn), wantMsg: "bd: test write"},
		{mode: string(doltAutoCommitBatch), wantMsg: ""},
		{mode: string(doltAutoCommitOff), wantMsg: ""},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			saveStorageMode(t)
			serverMode = true
			doltAutoCommit = tc.mode
			commandDidExplicitDoltCommit = false

			fake := &fakeTransactStore{}
			err := transactHonoringAutoCommit(context.Background(), fake, "bd: test write", func(tx storage.Transaction) error {
				return nil
			})
			if err != nil {
				t.Fatalf("transactHonoringAutoCommit: %v", err)
			}
			if len(fake.msgs) != 1 || fake.msgs[0] != tc.wantMsg {
				t.Fatalf("RunInTransaction messages = %q, want [%q] for mode %q", fake.msgs, tc.wantMsg, tc.mode)
			}
			wantExplicit := tc.wantMsg != ""
			if commandDidExplicitDoltCommit != wantExplicit {
				t.Fatalf("commandDidExplicitDoltCommit = %v, want %v for mode %q", commandDidExplicitDoltCommit, wantExplicit, tc.mode)
			}
		})
	}
}
