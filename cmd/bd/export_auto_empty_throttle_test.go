//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// fakeMovingStateStore is fakeHeadOnlyStore plus a settable working-set
// hash, so a test can model "the store changed" between two auto-export
// passes without a real Dolt write. searchCalls counts every SearchIssues,
// which only an export that got past change detection performs.
type fakeMovingStateStore struct {
	fakeHeadOnlyStore
	stateHash   string
	searchCalls int
}

func (f *fakeMovingStateStore) GetStateHash(_ context.Context) (string, error) {
	return f.stateHash, nil
}

func (f *fakeMovingStateStore) SearchIssues(ctx context.Context, q string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalls++
	return f.fakeHeadOnlyStore.SearchIssues(ctx, q, filter)
}

// TestMaybeAutoExport_EmptyExportDoesNotPrimeThrottle pins GH#6955's first
// automatic-export skip: following the documented opt-in on an empty
// workspace runs one auto-export that writes nothing, and that empty pass
// must not start an export.interval window. Base saved Timestamp: time.Now()
// there, so the first real `bd create` inside the interval was throttled and
// .beads/issues.jsonl never appeared until some later eligible write.
func TestMaybeAutoExport_EmptyExportDoesNotPrimeThrottle(t *testing.T) {
	t.Run("first write right after an empty pass exports", func(t *testing.T) {
		initConfigForTest(t)
		config.Set("export.auto", true)
		config.Set("export.interval", "1h")
		saveAndRestoreGlobals(t)
		fake := &fakeMovingStateStore{stateHash: "hash-empty"}
		store = fake
		dir := autoExportTestDir(t)
		beadsDir := filepath.Join(dir, ".beads")
		exportPath := filepath.Join(beadsDir, "issues.jsonl")

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("empty pass: %v", err)
		}
		if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
			t.Fatalf("an empty export must leave no file, stat err=%v", err)
		}
		state := loadExportAutoState(beadsDir)
		if state.LastDoltCommit != "hash-empty" {
			t.Fatalf("empty pass LastDoltCommit = %q, want %q (the hash must be kept so an unchanged empty store is not re-exported)", state.LastDoltCommit, "hash-empty")
		}
		if !shouldExport(state, time.Hour) {
			t.Fatalf("empty pass left Timestamp %v, which throttles the next export for export.interval=1h", state.Timestamp)
		}

		// The first durable create lands immediately after.
		fake.stateHash = "hash-one-issue"
		fake.issues = []*types.Issue{
			{ID: "thr-1", Title: "First real issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("pass after create: %v", err)
		}
		titles := loadIssueTitles(t, exportPath)
		if titles["thr-1"] != "First real issue" {
			t.Fatalf("issues.jsonl after the first create = %v, want thr-1 exported without a manual export (the empty pass primed the throttle)", titles)
		}
		if state := loadExportAutoState(beadsDir); state.Issues != 1 || state.LastDoltCommit != "hash-one-issue" {
			t.Fatalf("state after the real export = %+v, want Issues=1 and LastDoltCommit=hash-one-issue", state)
		}
	})

	t.Run("expired prior timestamp is kept, hash and anchor still advance", func(t *testing.T) {
		initConfigForTest(t)
		config.Set("export.auto", true)
		saveAndRestoreGlobals(t)
		fake := &fakeMovingStateStore{stateHash: "hash-now-empty"}
		store = fake
		dir := autoExportTestDir(t)
		beadsDir := filepath.Join(dir, ".beads")
		prior := time.Now().Add(-2 * time.Hour).Round(0).UTC()
		saveExportAutoState(beadsDir, &exportAutoState{
			LastDoltCommit: "hash-before",
			LastDiffAnchor: "anchor-before",
			Timestamp:      prior,
		})

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("empty pass: %v", err)
		}
		state := loadExportAutoState(beadsDir)
		if !state.Timestamp.Equal(prior) {
			t.Fatalf("empty pass Timestamp = %v, want the prior %v kept (an empty pass is not an export)", state.Timestamp, prior)
		}
		if state.LastDoltCommit != "hash-now-empty" {
			t.Fatalf("empty pass LastDoltCommit = %q, want %q", state.LastDoltCommit, "hash-now-empty")
		}
		if state.LastDiffAnchor != "head-commit-hash" {
			t.Fatalf("empty pass LastDiffAnchor = %q, want the real commit %q", state.LastDiffAnchor, "head-commit-hash")
		}
		if state.Issues != 0 || state.Memories != 0 {
			t.Fatalf("empty pass counts = %d/%d, want 0/0", state.Issues, state.Memories)
		}
	})

	t.Run("unchanged empty store is not exported again", func(t *testing.T) {
		initConfigForTest(t)
		config.Set("export.auto", true)
		saveAndRestoreGlobals(t)
		fake := &fakeMovingStateStore{stateHash: "hash-empty"}
		store = fake
		autoExportTestDir(t)

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("first empty pass: %v", err)
		}
		before := fake.searchCalls
		if before == 0 {
			t.Fatal("first empty pass never searched the store; the fixture is not reaching the export")
		}
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("second empty pass: %v", err)
		}
		if fake.searchCalls != before {
			t.Fatalf("SearchIssues calls went %d -> %d: an unchanged empty store must hit the state-hash shortcut", before, fake.searchCalls)
		}
	})
}
