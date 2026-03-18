//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestIsCrewActor verifies crew detection from actor paths.
func TestIsCrewActor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		actor string
		want  bool
	}{
		{"gastown/crew/zhora", true},
		{"cfutons/crew/melania", true},
		{"gastown/crew/max", true},
		{"gastown/polecats/furiosa", false},
		{"gastown/polecats/nux", false},
		{"hal", false},
		{"", false},
		{"crew", false},       // no slash
		{"a/crew", false},     // crew as last segment, not second-to-last
		{"a/crew/b/c", false}, // crew not in second-to-last position
		{"a/crew/b", true},    // canonical form
	}
	for _, tt := range tests {
		got := isCrewActor(tt.actor)
		if got != tt.want {
			t.Errorf("isCrewActor(%q) = %v, want %v", tt.actor, got, tt.want)
		}
	}
}

// TestGetInProgressBeads verifies in-progress bead lookup by assignee.
func TestGetInProgressBeads(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Set up: alice has one in_progress bead, bob has only open beads
	issues := []*types.Issue{
		{ID: "guard-alice-wip", Title: "Alice WIP", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask, Assignee: "gastown/crew/alice", CreatedAt: time.Now()},
		{ID: "guard-alice-open", Title: "Alice open", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Assignee: "gastown/crew/alice", CreatedAt: time.Now()},
		{ID: "guard-bob-open", Title: "Bob open", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "gastown/polecats/bob", CreatedAt: time.Now()},
	}
	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Alice has in_progress work
	aliceWork := getInProgressBeads(ctx, s, "gastown/crew/alice")
	if len(aliceWork) == 0 {
		t.Error("expected alice to have in_progress beads")
	}
	for _, i := range aliceWork {
		if i.Status != types.StatusInProgress {
			t.Errorf("expected in_progress status, got %q for %s", i.Status, i.ID)
		}
		if i.Assignee != "gastown/crew/alice" {
			t.Errorf("expected alice's bead, got assignee %q", i.Assignee)
		}
	}

	// Bob has no in_progress work
	bobWork := getInProgressBeads(ctx, s, "gastown/polecats/bob")
	if len(bobWork) != 0 {
		t.Errorf("expected bob to have no in_progress beads, got %d", len(bobWork))
	}

	// Unknown actor has no in_progress work
	unknownWork := getInProgressBeads(ctx, s, "nobody")
	if len(unknownWork) != 0 {
		t.Errorf("expected no in_progress beads for unknown actor, got %d", len(unknownWork))
	}
}

// TestApplyClaimGuard_CrewHardBlock verifies crew gets a non-empty error string.
func TestApplyClaimGuard_CrewHardBlock(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{
		ID: "guard-crew-wip", Title: "Crew WIP", Status: types.StatusInProgress, Priority: 1,
		IssueType: types.TypeTask, Assignee: "gastown/crew/zhora", CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	msg := applyClaimGuard(ctx, s, "gastown/crew/zhora", false)
	if msg == "" {
		t.Error("expected non-empty error message for crew hard block")
	}
	if msg != "You have open work: guard-crew-wip. Finish or hand off before claiming more." {
		t.Errorf("unexpected message: %q", msg)
	}
}

// TestApplyClaimGuard_PolecatSoftWarn verifies polecats get an empty return (soft warn only).
func TestApplyClaimGuard_PolecatSoftWarn(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{
		ID: "guard-polecat-wip", Title: "Polecat WIP", Status: types.StatusInProgress, Priority: 1,
		IssueType: types.TypeTask, Assignee: "gastown/polecats/furiosa", CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	msg := applyClaimGuard(ctx, s, "gastown/polecats/furiosa", false)
	if msg != "" {
		t.Errorf("expected empty return (soft warn) for polecat, got %q", msg)
	}
}

// TestApplyClaimGuard_AllowConcurrent verifies --allow-concurrent bypasses the guard.
func TestApplyClaimGuard_AllowConcurrent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{
		ID: "guard-allow-wip", Title: "WIP", Status: types.StatusInProgress, Priority: 1,
		IssueType: types.TypeTask, Assignee: "gastown/crew/zhora", CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	// With allowConcurrent=true, crew guard is bypassed
	msg := applyClaimGuard(ctx, s, "gastown/crew/zhora", true)
	if msg != "" {
		t.Errorf("expected empty return when allowConcurrent=true, got %q", msg)
	}
}

// TestApplyClaimGuard_NoInProgress verifies guard is silent when no in_progress beads exist.
func TestApplyClaimGuard_NoInProgress(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.CreateIssue(ctx, &types.Issue{
		ID: "guard-open-only", Title: "Open task", Status: types.StatusOpen, Priority: 1,
		IssueType: types.TypeTask, Assignee: "gastown/crew/zhora", CreatedAt: time.Now(),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	msg := applyClaimGuard(ctx, s, "gastown/crew/zhora", false)
	if msg != "" {
		t.Errorf("expected no guard message when no in_progress beads, got %q", msg)
	}
}

// TestApplyClaimGuard_EmptyActor verifies guard is skipped for empty actor.
func TestApplyClaimGuard_EmptyActor(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	msg := applyClaimGuard(ctx, s, "", false)
	if msg != "" {
		t.Errorf("expected no guard message for empty actor, got %q", msg)
	}
}
