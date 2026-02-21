//go:build cgo

package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// CheckEligibility tests
// =============================================================================

func TestCheckEligibility_IssueNotFound(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	eligible, reason, err := store.CheckEligibility(ctx, "nonexistent-id", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected not eligible for nonexistent issue")
	}
	if reason == "" {
		t.Error("expected a reason for ineligibility")
	}
}

func TestCheckEligibility_OpenIssueNotEligible(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-open",
		Title:     "Open Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected open issue to be ineligible for compaction")
	}
	if reason == "" {
		t.Error("expected reason explaining ineligibility")
	}
}

func TestCheckEligibility_RecentlyClosedNotEligible(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-recent",
		Title:     "Recently Closed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected recently closed issue to be ineligible for tier 1")
	}
	if reason == "" {
		t.Error("expected reason for ineligibility")
	}
}

func TestCheckEligibility_Tier1EligibleOldClosed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-old",
		Title:     "Old Closed Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Backdate closed_at to 45 days ago
	oldDate := time.Now().UTC().AddDate(0, 0, -45)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to backdate closed_at: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eligible {
		t.Errorf("expected old closed issue to be eligible for tier 1, reason: %s", reason)
	}
}

func TestCheckEligibility_Tier1AlreadyCompacted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-compact1",
		Title:     "Already Compacted",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Backdate and set compaction_level=1
	oldDate := time.Now().UTC().AddDate(0, 0, -45)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 1 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected already-compacted issue to be ineligible for tier 1")
	}
	if reason == "" {
		t.Error("expected reason for ineligibility")
	}
}

func TestCheckEligibility_Tier2Eligible(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-t2",
		Title:     "Tier 2 Candidate",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Set closed_at to 100 days ago and compaction_level=1
	oldDate := time.Now().UTC().AddDate(0, 0, -100)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 1 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eligible {
		t.Errorf("expected tier 2 eligible, reason: %s", reason)
	}
}

func TestCheckEligibility_Tier2NotCompactedFirst(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-t2-noprior",
		Title:     "Not Tier 1 Compacted",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Set closed_at to 100 days ago but keep compaction_level=0
	oldDate := time.Now().UTC().AddDate(0, 0, -100)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected ineligible for tier 2 without tier 1 first")
	}
	if reason == "" {
		t.Error("expected reason for ineligibility")
	}
}

func TestCheckEligibility_Tier2AlreadyCompacted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-t2-done",
		Title:     "Already Tier 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	oldDate := time.Now().UTC().AddDate(0, 0, -100)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 2 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected ineligible for already tier 2 compacted")
	}
	if reason == "" {
		t.Error("expected reason for ineligibility")
	}
}

func TestCheckEligibility_InvalidTier(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-badtier",
		Title:     "Bad Tier",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected ineligible for unsupported tier")
	}
	if reason == "" {
		t.Error("expected reason mentioning unsupported tier")
	}
}

func TestCheckEligibility_ClosedNoTimestamp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "elig-notime",
		Title:     "No Closed At",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Set status=closed but null out closed_at
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET status = 'closed', closed_at = NULL WHERE id = ?", issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	eligible, reason, err := store.CheckEligibility(ctx, issue.ID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eligible {
		t.Error("expected ineligible when closed_at is null")
	}
	if reason == "" {
		t.Error("expected reason about missing closed_at")
	}
}

// =============================================================================
// GetTier1Candidates tests
// =============================================================================

func TestGetTier1Candidates_Empty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates from empty store, got %d", len(candidates))
	}
}

func TestGetTier1Candidates_ReturnsEligible(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create and close an issue, then backdate it
	issue := &types.Issue{
		ID:          "t1-cand",
		Title:       "Tier 1 Candidate",
		Description: "Some description content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Backdate to 45 days ago
	oldDate := time.Now().UTC().AddDate(0, 0, -45)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to backdate: %v", err)
	}

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].IssueID != issue.ID {
		t.Errorf("expected issue %s, got %s", issue.ID, candidates[0].IssueID)
	}
	if candidates[0].OriginalSize <= 0 {
		t.Errorf("expected positive original size, got %d", candidates[0].OriginalSize)
	}
	if candidates[0].EstimatedSize <= 0 {
		t.Errorf("expected positive estimated size, got %d", candidates[0].EstimatedSize)
	}
}

func TestGetTier1Candidates_ExcludesRecentlyClosed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "t1-recent",
		Title:     "Recently Closed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}
	// closed_at is "now" -- should not appear in tier 1 candidates

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		if c.IssueID == issue.ID {
			t.Error("recently closed issue should not be a tier 1 candidate")
		}
	}
}

func TestGetTier1Candidates_ExcludesAlreadyCompacted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "t1-already",
		Title:     "Already Compacted",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	oldDate := time.Now().UTC().AddDate(0, 0, -45)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 1 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		if c.IssueID == issue.ID {
			t.Error("already tier 1 compacted issue should not be a candidate")
		}
	}
}

func TestGetTier1Candidates_ExcludesOpenIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "t1-open",
		Title:     "Still Open",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		if c.IssueID == issue.ID {
			t.Error("open issue should not be a tier 1 candidate")
		}
	}
}

// =============================================================================
// GetTier2Candidates tests
// =============================================================================

func TestGetTier2Candidates_Empty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	candidates, err := store.GetTier2Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates from empty store, got %d", len(candidates))
	}
}

func TestGetTier2Candidates_ReturnsEligible(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "t2-cand",
		Title:       "Tier 2 Candidate",
		Description: "Some tier 2 content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Backdate to 100 days ago and set compaction_level=1
	oldDate := time.Now().UTC().AddDate(0, 0, -100)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 1 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	candidates, err := store.GetTier2Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].IssueID != issue.ID {
		t.Errorf("expected issue %s, got %s", issue.ID, candidates[0].IssueID)
	}
}

func TestGetTier2Candidates_ExcludesNotYetTier1(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "t2-noprior",
		Title:     "Not Tier 1 Yet",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// 100 days ago but compaction_level=0
	oldDate := time.Now().UTC().AddDate(0, 0, -100)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	candidates, err := store.GetTier2Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		if c.IssueID == issue.ID {
			t.Error("issue without tier 1 compaction should not be a tier 2 candidate")
		}
	}
}

func TestGetTier2Candidates_ExcludesRecentTier1(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "t2-recent",
		Title:     "Recent Tier 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// 60 days ago with compaction_level=1 (not yet 90 days)
	oldDate := time.Now().UTC().AddDate(0, 0, -60)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ?, compaction_level = 1 WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	candidates, err := store.GetTier2Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		if c.IssueID == issue.ID {
			t.Error("issue closed only 60 days ago should not be a tier 2 candidate")
		}
	}
}

// =============================================================================
// ApplyCompaction tests
// =============================================================================

func TestApplyCompaction(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "compact-apply",
		Title:     "Apply Compaction",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	err := store.ApplyCompaction(ctx, issue.ID, 1, 5000, 1500, "abc123")
	if err != nil {
		t.Fatalf("failed to apply compaction: %v", err)
	}

	// Verify compaction metadata was recorded
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if retrieved.CompactionLevel != 1 {
		t.Errorf("expected compaction_level 1, got %d", retrieved.CompactionLevel)
	}
	if retrieved.OriginalSize != 5000 {
		t.Errorf("expected original_size 5000, got %d", retrieved.OriginalSize)
	}
	if retrieved.CompactedAtCommit == nil || *retrieved.CompactedAtCommit != "abc123" {
		t.Errorf("expected compacted_at_commit 'abc123', got %v", retrieved.CompactedAtCommit)
	}
	if retrieved.CompactedAt == nil {
		t.Error("expected compacted_at to be set")
	}
}

// =============================================================================
// GetTier1Candidates with dependencies
// =============================================================================

func TestGetTier1Candidates_DependentCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a closed issue (the candidate) and another open issue that depends on it
	candidate := &types.Issue{
		ID:          "t1-dep-cand",
		Title:       "Candidate With Deps",
		Description: "Has dependents",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	dependent := &types.Issue{
		ID:        "t1-dep-child",
		Title:     "Dependent Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{candidate, dependent} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	// Add blocking dependency: dependent blocks on candidate
	dep := &types.Dependency{
		IssueID:     dependent.ID,
		DependsOnID: candidate.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Close the candidate and backdate
	if err := store.CloseIssue(ctx, candidate.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}
	oldDate := time.Now().UTC().AddDate(0, 0, -45)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET closed_at = ? WHERE id = ?", oldDate, candidate.ID)
	if err != nil {
		t.Fatalf("failed to backdate: %v", err)
	}

	candidates, err := store.GetTier1Candidates(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].DependentCount != 1 {
		t.Errorf("expected dependent_count 1, got %d", candidates[0].DependentCount)
	}
}
