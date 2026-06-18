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
	messages        []string
	err             error
}

func (f *fakeConfigCommitStore) CommitConfigOnly(_ context.Context, message string) error {
	f.configOnlyCalls++
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
	if !strings.HasPrefix(fake.messages[0], "bd: remember (auto-commit) by ") {
		t.Fatalf("commit message = %q, want prefix %q", fake.messages[0], "bd: remember (auto-commit) by ")
	}
}

func TestCommitConfigWriteProxiedServerModeCommitsScoped(t *testing.T) {
	saveStorageMode(t)
	proxiedServerMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "config set"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 1 {
		t.Fatalf("CommitConfigOnly calls = %d, want 1 in proxied server mode", fake.configOnlyCalls)
	}
}

// Embedded mode already commits config via the '-Am' auto-commit and the
// unflagged-writes sweep — the helper must not double-commit there.
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
