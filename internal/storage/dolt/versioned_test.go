//go:build cgo

package dolt

import (
	"testing"
)

// TestCommitExists tests the CommitExists method.
func TestCommitExists(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Get the current commit hash (should exist after store initialization)
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("failed to get current commit: %v", err)
	}

	t.Run("valid commit hash returns true", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, currentCommit)
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if !exists {
			t.Errorf("expected commit %s to exist", currentCommit)
		}
	})

	t.Run("short hash prefix returns true", func(t *testing.T) {
		// Use first 8 characters as a short hash (like git's default short SHA)
		if len(currentCommit) < 8 {
			t.Skip("commit hash too short for prefix test")
		}
		shortHash := currentCommit[:8]
		exists, err := store.CommitExists(ctx, shortHash)
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if !exists {
			t.Errorf("expected short hash %s to match commit %s", shortHash, currentCommit)
		}
	})

	t.Run("invalid nonexistent commit returns false", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, "0000000000000000000000000000000000000000")
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if exists {
			t.Error("expected nonexistent commit to return false")
		}
	})

	t.Run("empty string returns false", func(t *testing.T) {
		exists, err := store.CommitExists(ctx, "")
		if err != nil {
			t.Fatalf("CommitExists failed: %v", err)
		}
		if exists {
			t.Error("expected empty string to return false")
		}
	})

	t.Run("malformed input returns false", func(t *testing.T) {
		testCases := []string{
			"invalid hash with spaces",
			"hash'with'quotes",
			"hash;injection",
			"hash--comment",
		}
		for _, tc := range testCases {
			exists, err := store.CommitExists(ctx, tc)
			if err != nil {
				t.Fatalf("CommitExists(%q) returned error: %v", tc, err)
			}
			if exists {
				t.Errorf("expected malformed input %q to return false", tc)
			}
		}
	})
}

