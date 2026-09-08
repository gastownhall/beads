package main

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestFilterClosedDeletionCandidatesRechecksClosedAtCutoff(t *testing.T) {
	cutoff := time.Date(2026, 3, 27, 20, 1, 44, 0, time.UTC)
	oldClosedAt := cutoff.Add(-time.Second)
	recentClosedAt := cutoff.Add(time.Second)

	candidates := []*types.Issue{
		{ID: "old-closed", Status: types.StatusClosed, ClosedAt: &oldClosedAt},
		{ID: "recent-closed", Status: types.StatusClosed, ClosedAt: &recentClosedAt},
		{ID: "missing-closed-at", Status: types.StatusClosed},
		{ID: "open-with-old-closed-at", Status: types.StatusOpen, ClosedAt: &oldClosedAt},
		{ID: "pinned-old", Status: types.StatusClosed, ClosedAt: &oldClosedAt, Pinned: true},
		nil,
	}

	filtered, stats := filterClosedDeletionCandidates(candidates, &cutoff)

	if len(filtered) != 1 || filtered[0].ID != "old-closed" {
		t.Fatalf("filtered IDs = %v, want only old-closed", closedDeletionCandidateIDs(filtered))
	}
	if stats.PinnedSkipped != 1 {
		t.Fatalf("PinnedSkipped = %d, want 1", stats.PinnedSkipped)
	}
	if stats.RecentClosedAtSkipped != 1 {
		t.Fatalf("RecentClosedAtSkipped = %d, want 1", stats.RecentClosedAtSkipped)
	}
	if stats.MissingClosedAtSkipped != 1 {
		t.Fatalf("MissingClosedAtSkipped = %d, want 1", stats.MissingClosedAtSkipped)
	}
	if stats.NonClosedSkipped != 1 {
		t.Fatalf("NonClosedSkipped = %d, want 1", stats.NonClosedSkipped)
	}
	if stats.NilSkipped != 1 {
		t.Fatalf("NilSkipped = %d, want 1", stats.NilSkipped)
	}
}

func TestFilterClosedDeletionCandidatesWithoutCutoffStillRequiresClosedAt(t *testing.T) {
	closedAt := time.Date(2026, 4, 26, 18, 30, 18, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "closed", Status: types.StatusClosed, ClosedAt: &closedAt},
		{ID: "closed-missing-time", Status: types.StatusClosed},
	}

	filtered, stats := filterClosedDeletionCandidates(candidates, nil)

	if len(filtered) != 1 || filtered[0].ID != "closed" {
		t.Fatalf("filtered IDs = %v, want only closed", closedDeletionCandidateIDs(filtered))
	}
	if stats.MissingClosedAtSkipped != 1 {
		t.Fatalf("MissingClosedAtSkipped = %d, want 1", stats.MissingClosedAtSkipped)
	}
	if stats.RecentClosedAtSkipped != 0 {
		t.Fatalf("RecentClosedAtSkipped = %d, want 0 without cutoff", stats.RecentClosedAtSkipped)
	}
}

func closedDeletionCandidateIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids
}
