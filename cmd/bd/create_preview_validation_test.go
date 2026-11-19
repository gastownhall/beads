package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func previewValidationIssue() *types.Issue {
	return &types.Issue{
		Title:     "PROBE",
		Status:    types.StatusOpen,
		IssueType: types.IssueType("task"),
	}
}

// TestCreatePreviewValidation pins the single-issue dry-run preview's insert
// validation (GH#5787 issue 2): a preview must pass the same
// PrepareIssueForInsert validation the real create runs — type and status
// against the destination vocabulary, field rules, closed_at stamping — on a
// copy, leaving the caller's issue untouched.
func TestCreatePreviewValidation(t *testing.T) {
	t.Run("invalid type is refused", func(t *testing.T) {
		issue := previewValidationIssue()
		issue.IssueType = types.IssueType("a long human title with spaces")
		err := validateCreatePreviewIssue(issue, createPreviewVocabulary{})
		if err == nil {
			t.Fatal("expected the invalid type to be refused")
		}
		if !strings.Contains(err.Error(), "invalid issue type") {
			t.Fatalf("expected an invalid-type diagnostic, got: %v", err)
		}
	})

	t.Run("custom type accepted only with the destination vocabulary", func(t *testing.T) {
		issue := previewValidationIssue()
		issue.IssueType = types.IssueType("incident")
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{}); err == nil {
			t.Fatal("expected a type registered nowhere to be refused")
		}
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{customTypes: []string{"incident"}}); err != nil {
			t.Fatalf("expected a type registered at the destination to be accepted, got: %v", err)
		}
	})

	t.Run("custom status accepted only with the destination vocabulary", func(t *testing.T) {
		issue := previewValidationIssue()
		issue.Status = types.Status("review")
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{}); err == nil {
			t.Fatal("expected a status registered nowhere to be refused")
		}
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{customStatuses: []string{"review"}}); err != nil {
			t.Fatalf("expected a status registered at the destination to be accepted, got: %v", err)
		}
	})

	t.Run("title, estimate and metadata field rules are shared with insert", func(t *testing.T) {
		emptyTitle := previewValidationIssue()
		emptyTitle.Title = ""
		if err := validateCreatePreviewIssue(emptyTitle, createPreviewVocabulary{}); err == nil {
			t.Fatal("expected an empty title to be refused")
		}

		negative := previewValidationIssue()
		minutes := -5
		negative.EstimatedMinutes = &minutes
		if err := validateCreatePreviewIssue(negative, createPreviewVocabulary{}); err == nil {
			t.Fatal("expected a negative estimate to be refused")
		}

		badMetadata := previewValidationIssue()
		badMetadata.Metadata = json.RawMessage(`{"a":`)
		err := validateCreatePreviewIssue(badMetadata, createPreviewVocabulary{})
		if err == nil {
			t.Fatal("expected invalid JSON metadata to be refused")
		}
		if !strings.Contains(err.Error(), "metadata") {
			t.Fatalf("expected a metadata diagnostic, got: %v", err)
		}
	})

	t.Run("closed_at is stamped on the copy, never the original", func(t *testing.T) {
		issue := previewValidationIssue()
		issue.Status = types.StatusClosed
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{}); err != nil {
			t.Fatalf("expected closed_at stamping to make a closed preview valid, got: %v", err)
		}
		if issue.ClosedAt != nil {
			t.Fatalf("validation must not stamp the caller's issue; closed_at = %v", issue.ClosedAt)
		}
		if !issue.CreatedAt.IsZero() {
			t.Fatalf("validation must not normalize the caller's issue; created_at = %v", issue.CreatedAt)
		}
	})

	t.Run("non-closed issue with closed_at is refused", func(t *testing.T) {
		issue := previewValidationIssue()
		closedAt := time.Now()
		issue.ClosedAt = &closedAt
		if err := validateCreatePreviewIssue(issue, createPreviewVocabulary{}); err == nil {
			t.Fatal("expected an open issue with closed_at to be refused")
		}
		if issue.ClosedAt == nil || !issue.ClosedAt.Equal(closedAt) {
			t.Fatalf("a refused preview must leave the original untouched; closed_at = %v", issue.ClosedAt)
		}
	})

	t.Run("built-in issue passes with an empty vocabulary", func(t *testing.T) {
		if err := validateCreatePreviewIssue(previewValidationIssue(), createPreviewVocabulary{}); err != nil {
			t.Fatalf("expected a minimal built-in preview to validate, got: %v", err)
		}
	})
}
