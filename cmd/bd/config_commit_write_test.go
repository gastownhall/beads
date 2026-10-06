//go:build cgo

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type fakeConfigCommitStore struct {
	storage.DoltStorage
	configOnlyCalls int
	userKVCalls     int
	messages        []string
	err             error
}

func (f *fakeConfigCommitStore) CommitConfigOnly(_ context.Context, message string) error {
	f.configOnlyCalls++
	f.messages = append(f.messages, message)
	return f.err
}

func (f *fakeConfigCommitStore) CommitConfigUserKVOnly(_ context.Context, message string) error {
	f.userKVCalls++
	f.messages = append(f.messages, message)
	return f.err
}

// GH#4078: server-mode config writes (bd remember, bd config set) must be
// committed immediately and scoped — maybeAutoCommit skips server mode
// entirely and generic Commit() excludes config, so nothing else ever
// commits them.
func TestCommitConfigWriteServerModeCommitsScoped(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 1 {
		t.Fatalf("CommitConfigOnly calls = %d, want 1 in server mode", fake.configOnlyCalls)
	}
	if fake.userKVCalls != 0 {
		t.Fatalf("CommitConfigUserKVOnly calls = %d, want 0 for a config write", fake.userKVCalls)
	}
	if !strings.HasPrefix(fake.messages[0], "bd: remember (auto-commit) by ") {
		t.Fatalf("commit message = %q, want prefix %q", fake.messages[0], "bd: remember (auto-commit) by ")
	}
}

// A memory write commits through the kv.*-screened variant, so a concurrent
// writer's dirty internal config key is refused instead of swept in.
func TestCommitMemoryWriteServerModeCommitsUserKVScreened(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitMemoryWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitMemoryWrite: %v", err)
	}
	if fake.userKVCalls != 1 {
		t.Fatalf("CommitConfigUserKVOnly calls = %d, want 1 in server mode", fake.userKVCalls)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 for a memory write", fake.configOnlyCalls)
	}
}

// The screen's refusal reaches the user rather than being taken for a
// nothing-to-commit no-op.
func TestCommitMemoryWriteSurfacesScreenRefusal(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: errors.New("refusing to commit 1 dirty internal config key(s)")}
	err := commitMemoryWrite(context.Background(), fake, "forget")
	if err == nil || !strings.Contains(err.Error(), "refusing to commit") {
		t.Fatalf("commitMemoryWrite error = %v, want the screen's refusal", err)
	}
}

// The proxied route's role commits each config write inside its own unit of
// work, so committing again here would be a second commit for the same write.
func TestCommitConfigWriteProxiedServerModeIsNoOp(t *testing.T) {
	saveStorageMode(t)
	proxiedServerMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "config set"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 in proxied server mode", fake.configOnlyCalls)
	}
}

// batch and off defer server-mode version commits to `bd dolt commit`, whose
// CommitAll includes config, so a config write must defer with them.
func TestCommitConfigWriteHonorsBatchAndOffModes(t *testing.T) {
	for _, mode := range []doltAutoCommitMode{doltAutoCommitBatch, doltAutoCommitOff} {
		t.Run(string(mode), func(t *testing.T) {
			saveStorageMode(t)
			serverMode = true
			doltAutoCommit = string(mode)

			fake := &fakeConfigCommitStore{}
			if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
				t.Fatalf("commitConfigWrite: %v", err)
			}
			if fake.configOnlyCalls != 0 {
				t.Fatalf("CommitConfigOnly calls = %d, want 0 in %s mode", fake.configOnlyCalls, mode)
			}
		})
	}
}

// Embedded mode already commits config through the PersistentPostRun
// auto-commit, so the helper must not double-commit there.
func TestCommitConfigWriteEmbeddedModeIsNoOp(t *testing.T) {
	saveStorageMode(t)
	serverMode = false
	proxiedServerMode = false

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 in embedded mode", fake.configOnlyCalls)
	}
}

// A real commit failure must surface — silently losing the commit is the
// exact bug class this helper exists to fix (GH#4078's silent no-op half).
func TestCommitConfigWriteSurfacesCommitError(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: errors.New("connection refused")}
	err := commitConfigWrite(context.Background(), fake, "remember")
	if err == nil {
		t.Fatal("commitConfigWrite returned nil, want the commit error surfaced")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "connection refused")
	}
}

// "Nothing to commit" is a benign race (e.g. the same key re-written to an
// identical value) and must not fail the user's write.
func TestCommitConfigWriteSwallowsNothingToCommit(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: errors.New("nothing to commit")}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v, want nothing-to-commit swallowed", err)
	}
	if fake.configOnlyCalls != 1 {
		t.Fatalf("CommitConfigOnly calls = %d, want 1", fake.configOnlyCalls)
	}
}

func TestCommitConfigWriteNilStoreIsNoOp(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	if err := commitConfigWrite(context.Background(), nil, "remember"); err != nil {
		t.Fatalf("commitConfigWrite with nil store: %v, want no-op", err)
	}
}
