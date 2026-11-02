package main

import (
	"reflect"
	"strings"
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
	if plan.refusal != nil {
		t.Errorf("refusal = %+v, want nil without recent commits", plan.refusal)
	}
}

// TestPlanCompactionRefusesStraddledCutoff covers #6645: a preserved commit
// that is an ancestor of an old commit makes the date partition unsafe to
// replay, whatever the dates look like. Entries are in Log order (date DESC).
func TestPlanCompactionRefusesStraddledCutoff(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-time.Hour)

	tests := []struct {
		name    string
		entries []storage.CommitInfo
		// wantRefusal names the expected recent/old hashes; empty = no refusal.
		wantRefusal string
	}{
		{
			// The reported shape: recent A (dated future) is the PARENT of
			// old B (dated past the cutoff), and B undid A's edit.
			name: "recent ancestor of the boundary",
			entries: []storage.CommitInfo{
				{Hash: "A", Date: now.Add(40 * time.Minute), CommitOrder: 4},
				{Hash: "B", Date: now.Add(-90 * time.Minute), CommitOrder: 5},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
				{Hash: "root", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			wantRefusal: "A/B",
		},
		{
			// The straddling recent commit need not sit at the tip: any
			// preserved commit that an old commit descends from is unsafe.
			// The refusal names the lowest-order recent and the highest-order
			// old commit (the boundary).
			name: "mid-chain straddle",
			entries: []storage.CommitInfo{
				{Hash: "tip", Date: now.Add(-80 * time.Minute), CommitOrder: 6},
				{Hash: "A", Date: now.Add(40 * time.Minute), CommitOrder: 3},
				{Hash: "B", Date: now.Add(-90 * time.Minute), CommitOrder: 4},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
				{Hash: "root", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			wantRefusal: "A/tip",
		},
		{
			// Equal commit order is conservatively unsafe: this linear-history
			// rule cannot prove arbitrary ancestry.
			name: "equal order",
			entries: []storage.CommitInfo{
				{Hash: "A", Date: now.Add(40 * time.Minute), CommitOrder: 4},
				{Hash: "B", Date: now.Add(-90 * time.Minute), CommitOrder: 4},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
				{Hash: "root", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			wantRefusal: "A/B",
		},
		{
			// A recent-dated ROOT with old descendants is the same hazard
			// upside down: replaying the root onto its own descendants.
			name: "recent root",
			entries: []storage.CommitInfo{
				{Hash: "root", Date: now.Add(40 * time.Minute), CommitOrder: 1},
				{Hash: "old2", Date: now.Add(-90 * time.Minute), CommitOrder: 3},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
			},
			wantRefusal: "root/old2",
		},
		{
			// Dates misordered entirely WITHIN a partition are safe (#6516
			// handles their replay order): every old commit still precedes
			// every recent one in commit order.
			name: "misdated old window is not a refusal",
			entries: []storage.CommitInfo{
				{Hash: "R", Date: now.Add(time.Hour), CommitOrder: 6},
				{Hash: "C", Date: now.Add(-70 * time.Minute), CommitOrder: 3},
				{Hash: "E1", Date: now.Add(-5 * time.Hour), CommitOrder: 4},
				{Hash: "E2", Date: now.Add(-4 * time.Hour), CommitOrder: 5},
				{Hash: "root", Date: now.Add(-6 * time.Hour), CommitOrder: 1},
			},
			wantRefusal: "",
		},
		{
			// A commit dated exactly at the cutoff is recent (membership is
			// Date.Before), but here it still postdates every old commit.
			name: "exact cutoff membership",
			entries: []storage.CommitInfo{
				{Hash: "A", Date: cutoff, CommitOrder: 4},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
				{Hash: "root", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			wantRefusal: "",
		},
		{
			// With at most one old commit there is nothing to squash: the
			// plan stays a no-op even though the partition straddles.
			name: "one old commit stays a no-op",
			entries: []storage.CommitInfo{
				{Hash: "A", Date: now.Add(40 * time.Minute), CommitOrder: 4},
				{Hash: "B", Date: now.Add(-90 * time.Minute), CommitOrder: 5},
			},
			wantRefusal: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planCompaction(tt.entries, cutoff)
			if tt.wantRefusal == "" {
				if plan.refusal != nil {
					t.Fatalf("refusal = %+v, want nil", plan.refusal)
				}
				return
			}
			if plan.refusal == nil {
				t.Fatalf("refusal = nil, want %s", tt.wantRefusal)
			}
			wantRecent, wantOld, _ := strings.Cut(tt.wantRefusal, "/")
			if plan.refusal.recent.Hash != wantRecent || plan.refusal.old.Hash != wantOld {
				t.Errorf("refusal = %s/%s, want %s", plan.refusal.recent.Hash, plan.refusal.old.Hash, tt.wantRefusal)
			}
		})
	}
}

// TestCompactRefusalDiagnostic pins the shared refusal text: both compact
// front doors print exactly this, so the hashes, dates and hint a user is
// told to act on cannot drift between embedded, server and proxied-server
// workspaces (#6645).
func TestCompactRefusalDiagnostic(t *testing.T) {
	cutoff := time.Date(2026, 11, 2, 0, 0, 0, 0, time.FixedZone("CET", 3600))
	r := &compactRefusal{
		recent: storage.CommitInfo{Hash: "recenthash0123", Date: time.Date(2026, 11, 2, 1, 30, 0, 0, time.UTC)},
		old:    storage.CommitInfo{Hash: "oldhash456789", Date: time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)},
	}
	message, hint := compactRefusalDiagnostic(cutoff, r)

	for _, want := range []string{
		"cannot compact: commit dates are out of history order across cutoff 2026-11-01T23:00:00Z",
		"recent commit recentha (dated 2026-11-02T01:30:00Z)",
		"is not after old commit oldhash4 (dated 2026-11-01T23:00:00Z) in commit order",
		"replay could change data",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message missing %q:\n%s", want, message)
		}
	}
	for _, want := range []string{"No compaction changes were made", "--days", "--dry-run"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q:\n%s", want, hint)
		}
	}
}
