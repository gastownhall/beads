package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestPlanCompactionReplaysInCommitOrder covers #6516: a child dated before its
// parent must still be replayed after it. Entries are in Log order (date DESC).
func TestPlanCompactionReplaysInCommitOrder(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	entries := []storage.CommitInfo{
		{Hash: "parent", Date: now.Add(40 * time.Minute), CommitOrder: 4},
		{Hash: "child", Date: now.Add(20 * time.Minute), CommitOrder: 5}, // dated before its parent
		{Hash: "mid", Date: now.Add(-30 * time.Minute), CommitOrder: 3},
		{Hash: "old2", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
		{Hash: "old1", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
	}
	// Log order is date DESC: parent(+40m), child(+20m), mid, old2, old1.
	plan := planCompaction(entries, cutoff)

	want := []string{"mid", "parent", "child"}
	if !reflect.DeepEqual(plan.recentHashes, want) {
		t.Errorf("recentHashes = %v, want %v (ascending commit_order)", plan.recentHashes, want)
	}
	if plan.oldCommits != 2 {
		t.Errorf("oldCommits = %d, want 2", plan.oldCommits)
	}
	// Partition selection stays date-based.
	if plan.boundaryHash != "old2" {
		t.Errorf("boundaryHash = %q, want old2", plan.boundaryHash)
	}
	if plan.initialHash != "old1" {
		t.Errorf("initialHash = %q, want old1", plan.initialHash)
	}
}

func TestPlanCompactionNoRecent(t *testing.T) {
	now := time.Now()
	plan := planCompaction([]storage.CommitInfo{
		{Hash: "b", Date: now.Add(-time.Hour), CommitOrder: 2},
		{Hash: "a", Date: now.Add(-2 * time.Hour), CommitOrder: 1},
	}, now)
	if len(plan.recentHashes) != 0 || plan.oldCommits != 2 || plan.boundaryHash != "b" || plan.initialHash != "a" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}
