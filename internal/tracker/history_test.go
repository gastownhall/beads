//go:build cgo

package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestSyncProducesHistoryEntry(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mt := newMockTracker("test-tracker")
	mt.issues = []TrackerIssue{
		{
			ID:         "ext-1",
			Identifier: "EXT-1",
			Title:      "Test issue 1",
			URL:        "https://test-tracker.test/EXT-1",
			UpdatedAt:  time.Now(),
		},
	}

	engine := NewEngine(mt, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("Sync not successful: %s", result.Error)
	}

	db := store.DB()
	entries, err := QuerySyncHistory(ctx, db, "test-tracker", nil, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one sync history entry")
	}

	entry := entries[0]
	if entry.Tracker != "test-tracker" {
		t.Errorf("expected tracker 'test-tracker', got %q", entry.Tracker)
	}
	if entry.Direction != "pull" {
		t.Errorf("expected direction 'pull', got %q", entry.Direction)
	}
	if !entry.Success {
		t.Errorf("expected success=true")
	}
	if entry.Actor != "test-actor" {
		t.Errorf("expected actor 'test-actor', got %q", entry.Actor)
	}
	if entry.IssuesCreated != 1 {
		t.Errorf("expected issues_created=1, got %d", entry.IssuesCreated)
	}
	if entry.CompletedAt.Before(entry.StartedAt) {
		t.Errorf("completed_at should be after started_at")
	}
}

func TestSyncHistoryItems(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mt := newMockTracker("test-tracker")
	mt.issues = []TrackerIssue{
		{
			ID:         "ext-1",
			Identifier: "EXT-1",
			Title:      "Test issue 1",
			URL:        "https://test-tracker.test/EXT-1",
			UpdatedAt:  time.Now(),
		},
		{
			ID:         "ext-2",
			Identifier: "EXT-2",
			Title:      "Test issue 2",
			URL:        "https://test-tracker.test/EXT-2",
			UpdatedAt:  time.Now(),
		},
	}

	engine := NewEngine(mt, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if result.Stats.Created != 2 {
		t.Fatalf("expected 2 created, got %d", result.Stats.Created)
	}

	db := store.DB()
	entries, err := QuerySyncHistory(ctx, db, "", nil, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one sync history entry")
	}

	items, err := QuerySyncHistoryItems(ctx, db, entries[0].SyncRunID)
	if err != nil {
		t.Fatalf("QuerySyncHistoryItems failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(items))
	}

	for _, item := range items {
		if item.Outcome != "created" {
			t.Errorf("expected outcome 'created', got %q for bead %s", item.Outcome, item.BeadID)
		}
		if item.SyncRunID != entries[0].SyncRunID {
			t.Errorf("sync_run_id mismatch")
		}
	}
}

func TestSyncHistorySinceFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	db := store.DB()

	past := time.Now().UTC().Add(-2 * time.Hour)
	future := time.Now().UTC().Add(2 * time.Hour)

	err := RecordSyncHistory(ctx, db, &SyncHistoryEntry{
		SyncRunID:   "run-old",
		StartedAt:   past,
		CompletedAt: past.Add(time.Second),
		Tracker:     "Linear",
		Direction:   "push",
		Success:     true,
	})
	if err != nil {
		t.Fatalf("RecordSyncHistory (old): %v", err)
	}

	err = RecordSyncHistory(ctx, db, &SyncHistoryEntry{
		SyncRunID:   "run-new",
		StartedAt:   future,
		CompletedAt: future.Add(time.Second),
		Tracker:     "Linear",
		Direction:   "pull",
		Success:     true,
	})
	if err != nil {
		t.Fatalf("RecordSyncHistory (new): %v", err)
	}

	// Query with --since between the two entries
	cutoff := time.Now().UTC()
	entries, err := QuerySyncHistory(ctx, db, "", &cutoff, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory --since: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after filter, got %d", len(entries))
	}
	if entries[0].SyncRunID != "run-new" {
		t.Errorf("expected run-new, got %s", entries[0].SyncRunID)
	}
}

func TestSyncHistoryPersistsAcrossInvocations(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mt := newMockTracker("test-tracker")
	mt.issues = []TrackerIssue{
		{
			ID:         "ext-1",
			Identifier: "EXT-1",
			Title:      "First run",
			URL:        "https://test-tracker.test/EXT-1",
			UpdatedAt:  time.Now(),
		},
	}

	engine := NewEngine(mt, store, "test-actor")

	// First sync
	_, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync 1 failed: %v", err)
	}

	// Second sync (push the issue we just pulled)
	mt.issues = nil
	_, err = engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync 2 failed: %v", err)
	}

	db := store.DB()
	entries, err := QuerySyncHistory(ctx, db, "", nil, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory failed: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(entries))
	}
}

func TestDryRunDoesNotRecordHistory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mt := newMockTracker("test-tracker")
	mt.issues = []TrackerIssue{
		{
			ID:         "ext-1",
			Identifier: "EXT-1",
			Title:      "Dry run issue",
			URL:        "https://test-tracker.test/EXT-1",
			UpdatedAt:  time.Now(),
		},
	}

	engine := NewEngine(mt, store, "test-actor")
	_, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync (dry-run) failed: %v", err)
	}

	db := store.DB()
	entries, err := QuerySyncHistory(ctx, db, "", nil, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 history entries for dry-run, got %d", len(entries))
	}
}

func TestSyncHistoryRecordsPushOutcomes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create a local issue to push
	issue := &types.Issue{
		ID:        "bd-push1",
		Title:     "Push test",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	mt := newMockTracker("test-tracker")
	engine := NewEngine(mt, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ShouldPush: func(i *types.Issue) bool { return true },
	}

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync (push) failed: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Fatalf("expected 1 created, got %d", result.Stats.Created)
	}

	db := store.DB()
	entries, err := QuerySyncHistory(ctx, db, "", nil, 10)
	if err != nil {
		t.Fatalf("QuerySyncHistory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected sync history entry")
	}

	if entries[0].Direction != "push" {
		t.Errorf("expected direction 'push', got %q", entries[0].Direction)
	}
	if entries[0].IssuesCreated != 1 {
		t.Errorf("expected issues_created=1, got %d", entries[0].IssuesCreated)
	}

	items, err := QuerySyncHistoryItems(ctx, db, entries[0].SyncRunID)
	if err != nil {
		t.Fatalf("QuerySyncHistoryItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Outcome != "created" {
		t.Errorf("expected outcome 'created', got %q", items[0].Outcome)
	}
	if items[0].BeadID != "bd-push1" {
		t.Errorf("expected bead_id 'bd-push1', got %q", items[0].BeadID)
	}
}
