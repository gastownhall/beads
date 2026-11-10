//go:build cgo

package main

// GH#6163: maybeAutoExport's failure policy. Operational failures return an
// error the caller reports once (exit 1); deliberate safety refusals and an
// unsupported state hash stay warning-only skips. Kept apart from
// export_auto_test.go so patches appended to that file still apply.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// seedAutoExportFailureFixture makes an auto-export workspace whose JSONL
// holds seed (none when seed is "") and whose export-state.json holds state,
// and returns the JSONL path and the state file's exact bytes.
func seedAutoExportFailureFixture(t *testing.T, seed string, state *exportAutoState) (beadsDir, jsonl string, stateBytes []byte) {
	t.Helper()
	beadsDir = filepath.Join(autoExportTestDir(t), ".beads")
	jsonl = filepath.Join(beadsDir, "issues.jsonl")
	if seed != "" {
		if err := os.WriteFile(jsonl, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saveExportAutoState(beadsDir, state)
	stateBytes, err := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
	if err != nil {
		t.Fatal(err)
	}
	return beadsDir, jsonl, stateBytes
}

// assertAutoExportUntouched checks that a failed or skipped auto-export left
// the JSONL exactly as seeded (absent when seed is "") and export-state.json
// byte for byte, timestamp included, so the next eligible command retries.
func assertAutoExportUntouched(t *testing.T, beadsDir, jsonl, seed string, stateBytes []byte) {
	t.Helper()
	if seed == "" {
		if _, err := os.Stat(jsonl); !os.IsNotExist(err) {
			t.Fatalf("%s was created (stat err %v), want no JSONL", jsonl, err)
		}
	} else {
		assertFileUnchanged(t, jsonl, seed)
	}
	got, err := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
	if err != nil || !bytes.Equal(got, stateBytes) {
		t.Fatalf("export state changed: got %s (err %v), want %s", got, err, stateBytes)
	}
}

// failingStateHashStore is a StateHasher whose state hash read fails.
type failingStateHashStore struct {
	fakeStateHashStore
	err         error
	searchCalls int
}

func (f *failingStateHashStore) GetStateHash(context.Context) (string, error) {
	f.stateHashCalls++
	return "", f.err
}

func (f *failingStateHashStore) SearchIssues(ctx context.Context, q string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	return f.fakeStateHashStore.SearchIssues(ctx, q, filter)
}

// failingHeadCommitStore is no StateHasher, so its failing GetCurrentCommit
// is the state hash read.
type failingHeadCommitStore struct {
	fakeHeadOnlyStore
	err error
}

func (f *failingHeadCommitStore) GetCurrentCommit(context.Context) (string, error) {
	f.currentCommitCalls++
	return "", f.err
}

// GH#6163: an ordinary state-hash failure fails the auto-export instead of
// warning and returning nil while the JSONL drifts from the store. It is
// checked before the throttle, so a recent export does not hide it.
func TestMaybeAutoExportFailsWhenStateHashUnreadable(t *testing.T) {
	cases := []struct {
		name  string
		store func(error) storage.DoltStorage
		seed  string
	}{
		{
			name:  "state hasher",
			store: func(err error) storage.DoltStorage { return &failingStateHashStore{err: err} },
			seed:  exportRelationSeed,
		},
		{
			name:  "head commit fallback",
			store: func(err error) storage.DoltStorage { return &failingHeadCommitStore{err: err} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			sentinel := errors.New("injected state hash failure")
			store = tc.store(sentinel)
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, tc.seed, &exportAutoState{
				LastDoltCommit: "hash-before-write",
				Timestamp:      time.Now(),
			})

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if !errors.Is(err, sentinel) {
				t.Fatalf("maybeAutoExport err = %v, want errors.Is %v", err, sentinel)
			}
			if !strings.Contains(err.Error(), jsonl) {
				t.Fatalf("maybeAutoExport err = %q, want the export destination %q", err, jsonl)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want nothing: the caller reports the failure once", stderr)
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)
		})
	}
}
