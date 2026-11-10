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

// queryFailingStore fails the failOn-th SearchIssues call. With one in-scope
// issue in both the store and the JSONL, call 1 is the reconciliation query
// and call 2 is the empty-overwrite check.
type queryFailingStore struct {
	fakeStateHashStore
	failOn      int
	err         error
	searchCalls int
}

func (f *queryFailingStore) SearchIssues(ctx context.Context, q string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	if f.searchCalls == f.failOn {
		return nil, f.err
	}
	return f.fakeStateHashStore.SearchIssues(ctx, q, filter)
}

// GH#6163: every pre-export check failure — an unreadable or invalid JSONL,
// or a failing store query in either guard — fails the auto-export with the
// destination named, prints nothing itself (the caller reports it once), and
// leaves the JSONL and export state alone.
func TestMaybeAutoExportPrecheckFailureIsAnError(t *testing.T) {
	sentinel := errors.New("injected search failure")
	issue := &types.Issue{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}
	cases := []struct {
		name      string
		failOn    int
		seed      string
		dirAtPath bool
		wantErr   error
		wantCalls int
		wantText  string
	}{
		{name: "reconciliation query", failOn: 1, seed: exportRelationSeed, wantErr: sentinel, wantCalls: 1, wantText: "compare existing JSONL against local store"},
		{name: "empty check query", failOn: 2, seed: exportRelationSeed, wantErr: sentinel, wantCalls: 2, wantText: "check for an empty export over existing JSONL"},
		{name: "malformed JSONL", seed: "{not json\n", wantText: "compare existing JSONL against local store"},
		{name: "unreadable JSONL", dirAtPath: true, wantText: "compare existing JSONL against local store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			fake := &queryFailingStore{
				fakeStateHashStore: fakeStateHashStore{stateHash: "hash-after-write", issues: []*types.Issue{issue}},
				failOn:             tc.failOn,
				err:                sentinel,
			}
			store = fake
			beadsDir, jsonl, stateBytes := seedAutoExportFailureFixture(t, tc.seed, &exportAutoState{LastDoltCommit: "hash-before-write"})
			if tc.dirAtPath {
				// Opens fine, then fails on read.
				if err := os.Mkdir(jsonl, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err == nil {
				t.Fatal("maybeAutoExport err = nil, want the pre-export check failure")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("maybeAutoExport err = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), jsonl) || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("maybeAutoExport err = %q, want %q naming %q", err, tc.wantText, jsonl)
			}
			if tc.wantCalls != 0 && fake.searchCalls != tc.wantCalls {
				t.Fatalf("SearchIssues calls = %d, want %d", fake.searchCalls, tc.wantCalls)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want nothing: the caller reports the failure once", stderr)
			}
			if tc.dirAtPath {
				if fi, statErr := os.Stat(jsonl); statErr != nil || !fi.IsDir() {
					t.Fatalf("%s replaced (stat %v, err %v), want the directory left alone", jsonl, fi, statErr)
				}
				got, readErr := os.ReadFile(filepath.Join(beadsDir, exportAutoStateFile))
				if readErr != nil || !bytes.Equal(got, stateBytes) {
					t.Fatalf("export state changed: got %s (err %v), want %s", got, readErr, stateBytes)
				}
				return
			}
			assertAutoExportUntouched(t, beadsDir, jsonl, tc.seed, stateBytes)
		})
	}
}
