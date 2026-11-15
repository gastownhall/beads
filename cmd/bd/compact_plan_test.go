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
	// Boundary and root are the highest-order old and lowest-order commits.
	if plan.boundaryHash != "old2" {
		t.Errorf("boundaryHash = %q, want old2", plan.boundaryHash)
	}
	if plan.initialHash != "old1" {
		t.Errorf("initialHash = %q, want old1", plan.initialHash)
	}
	// The head is the highest-order commit, though parent is dated after it.
	if plan.headHash != "child" {
		t.Errorf("headHash = %q, want child", plan.headHash)
	}
}

func TestPlanCompactionNoRecent(t *testing.T) {
	now := time.Now()
	plan := planCompaction([]storage.CommitInfo{
		{Hash: "b", Date: now.Add(-time.Hour), CommitOrder: 2},
		{Hash: "a", Date: now.Add(-2 * time.Hour), CommitOrder: 1},
	}, now)
	if len(plan.recentHashes) != 0 || plan.oldCommits != 2 || plan.boundaryHash != "b" || plan.initialHash != "a" || plan.headHash != "b" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}

// TestPlanCompactionRefusesStraddledCutoff covers #6645: a recent commit at or
// below the highest-order old commit is already in the squashed base, and
// replaying it again can change data, so the plan carries a refusal naming
// the lowest-order recent and highest-order old commits.
func TestPlanCompactionRefusesStraddledCutoff(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cutoff := now
	tests := []struct {
		name                string
		entries             []storage.CommitInfo
		wantRecent, wantOld string
	}{
		{
			// The reporter's shape: recent A, then its old-dated child B at the tip.
			name: "tip",
			entries: []storage.CommitInfo{
				{Hash: "aaaaaaaaaa", Date: now.Add(40 * time.Minute), CommitOrder: 3},
				{Hash: "bbbbbbbbbb", Date: now.Add(-time.Minute), CommitOrder: 4},
				{Hash: "old1", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
				{Hash: "root", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			wantRecent: "aaaaaaaaaa", wantOld: "bbbbbbbbbb",
		},
		{
			name: "mid_chain",
			entries: []storage.CommitInfo{
				{Hash: "r2", Date: now.Add(2 * time.Hour), CommitOrder: 5},
				{Hash: "r1", Date: now.Add(time.Hour), CommitOrder: 2},
				{Hash: "o3", Date: now.Add(-time.Minute), CommitOrder: 4},
				{Hash: "o2", Date: now.Add(-2 * time.Minute), CommitOrder: 3},
				{Hash: "root", Date: now.Add(-time.Hour), CommitOrder: 1},
			},
			wantRecent: "r1", wantOld: "o3",
		},
		{
			name: "equal_order",
			entries: []storage.CommitInfo{
				{Hash: "r", Date: now.Add(time.Hour), CommitOrder: 3},
				{Hash: "o", Date: now.Add(-time.Minute), CommitOrder: 3},
				{Hash: "root", Date: now.Add(-time.Hour), CommitOrder: 1},
			},
			wantRecent: "r", wantOld: "o",
		},
		{
			name: "recent_root",
			entries: []storage.CommitInfo{
				{Hash: "root", Date: now.Add(time.Hour), CommitOrder: 1},
				{Hash: "o2", Date: now.Add(-time.Minute), CommitOrder: 3},
				{Hash: "o1", Date: now.Add(-2 * time.Minute), CommitOrder: 2},
			},
			wantRecent: "root", wantOld: "o2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planCompaction(tt.entries, cutoff)
			if plan.refusal == nil {
				t.Fatalf("plan.refusal = nil, want refusal of %s vs %s", tt.wantRecent, tt.wantOld)
			}
			if plan.refusal.recent.Hash != tt.wantRecent || plan.refusal.old.Hash != tt.wantOld {
				t.Errorf("refusal names recent %s / old %s, want %s / %s",
					plan.refusal.recent.Hash, plan.refusal.old.Hash, tt.wantRecent, tt.wantOld)
			}
		})
	}
}

func TestPlanCompactionRefusalMessage(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	plan := planCompaction([]storage.CommitInfo{
		{Hash: "aaaaaaaa11112222", Date: now.Add(40 * time.Minute), CommitOrder: 3},
		{Hash: "bbbbbbbb33334444", Date: now.Add(-time.Minute), CommitOrder: 4},
		{Hash: "root", Date: now.Add(-time.Hour), CommitOrder: 1},
	}, now)
	if plan.refusal == nil {
		t.Fatal("plan.refusal = nil, want a refusal")
	}
	want := "cannot compact: commit dates are out of history order across cutoff 2026-11-02T12:00:00Z: " +
		"recent commit aaaaaaaa (dated 2026-11-02T12:40:00Z) is not after old commit bbbbbbbb " +
		"(dated 2026-11-02T11:59:00Z) in commit order; replay could change data"
	if got := plan.refusal.message(); got != want {
		t.Errorf("message() =\n%s\nwant\n%s", got, want)
	}
}

// TestPlanCompactionNoRefusal pins the plans that stay unrefused: contiguous
// partitions, no recent commits, and no-op plans with at most one old commit.
func TestPlanCompactionNoRefusal(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		entries []storage.CommitInfo
	}{
		{"contiguous", []storage.CommitInfo{
			{Hash: "r", Date: now.Add(time.Hour), CommitOrder: 3},
			{Hash: "o", Date: now.Add(-time.Minute), CommitOrder: 2},
			{Hash: "root", Date: now.Add(-time.Hour), CommitOrder: 1},
		}},
		{"exact_cutoff_is_recent", []storage.CommitInfo{
			{Hash: "r", Date: now, CommitOrder: 3},
			{Hash: "o", Date: now.Add(-time.Minute), CommitOrder: 2},
			{Hash: "root", Date: now.Add(-time.Hour), CommitOrder: 1},
		}},
		{"no_recent", []storage.CommitInfo{
			{Hash: "o", Date: now.Add(-time.Hour), CommitOrder: 1},
			{Hash: "o2", Date: now.Add(-2 * time.Hour), CommitOrder: 2},
		}},
		{"one_old_noop", []storage.CommitInfo{
			{Hash: "r", Date: now.Add(time.Hour), CommitOrder: 1},
			{Hash: "o", Date: now.Add(-time.Minute), CommitOrder: 2},
		}},
		{"zero_old_noop", []storage.CommitInfo{
			{Hash: "r2", Date: now.Add(2 * time.Hour), CommitOrder: 1},
			{Hash: "r1", Date: now.Add(time.Hour), CommitOrder: 2},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if plan := planCompaction(tt.entries, now); plan.refusal != nil {
				t.Errorf("unexpected refusal: %s", plan.refusal.message())
			}
		})
	}
}

// TestPlanCompactionSelectsBoundaryAndRootByCommitOrder: within the old window
// dates can be out of history order, so the squashed base must end at the
// highest-order old commit and start at the lowest-order commit, not at the
// newest- and oldest-dated ones.
func TestPlanCompactionSelectsBoundaryAndRootByCommitOrder(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	// Log order is date DESC: r, c, root, e2, e1.
	plan := planCompaction([]storage.CommitInfo{
		{Hash: "r", Date: now.Add(time.Hour), CommitOrder: 5},
		{Hash: "c", Date: now.Add(-time.Minute), CommitOrder: 2},
		{Hash: "root", Date: now.Add(-2 * time.Hour), CommitOrder: 1},
		{Hash: "e2", Date: now.Add(-4 * time.Hour), CommitOrder: 4},
		{Hash: "e1", Date: now.Add(-5 * time.Hour), CommitOrder: 3},
	}, now)
	if plan.boundaryHash != "e2" {
		t.Errorf("boundaryHash = %q, want e2 (highest-order old commit)", plan.boundaryHash)
	}
	if plan.initialHash != "root" {
		t.Errorf("initialHash = %q, want root (lowest-order commit)", plan.initialHash)
	}
	if plan.oldCommits != 4 || !reflect.DeepEqual(plan.recentHashes, []string{"r"}) {
		t.Errorf("oldCommits = %d, recentHashes = %v; want 4, [r]", plan.oldCommits, plan.recentHashes)
	}
	if plan.refusal != nil {
		t.Errorf("unexpected refusal: %s", plan.refusal.message())
	}
}

// TestPlanCompactionHeadIsHighestCommitOrder: Compact verifies its replay
// against the plan's head, the HEAD the log was read from. That is the
// highest-order commit, not the newest-dated one; a clock that jumped back
// can date HEAD before its ancestors, in either window.
func TestPlanCompactionHeadIsHighestCommitOrder(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		entries []storage.CommitInfo
		want    string
	}{
		{
			name: "recent head dated before a recent ancestor",
			entries: []storage.CommitInfo{
				{Hash: "r1", Date: now.Add(2 * time.Hour), CommitOrder: 3},
				{Hash: "head", Date: now.Add(time.Hour), CommitOrder: 4},
				{Hash: "o2", Date: now.Add(-time.Hour), CommitOrder: 2},
				{Hash: "o1", Date: now.Add(-2 * time.Hour), CommitOrder: 1},
			},
			want: "head",
		},
		{
			name: "old head dated before an old ancestor",
			entries: []storage.CommitInfo{
				{Hash: "o2", Date: now.Add(-time.Hour), CommitOrder: 2},
				{Hash: "head", Date: now.Add(-2 * time.Hour), CommitOrder: 3},
				{Hash: "o1", Date: now.Add(-3 * time.Hour), CommitOrder: 1},
			},
			want: "head",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planCompaction(tt.entries, now).headHash; got != tt.want {
				t.Errorf("headHash = %q, want %q", got, tt.want)
			}
		})
	}
}
