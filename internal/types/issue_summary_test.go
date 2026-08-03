package types

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestIssueSummaryJSONTagsMatchIssue pins IssueSummary's wire shape to Issue's,
// field by field. IssueSummary is a narrow projection of Issue, so any field it
// carries must serialize under the same key with the same omitempty behavior —
// otherwise a summary-backed `bd list --json` and a full-Issue-backed one emit
// different documents for the same bead. Comparing against Issue's own tags
// rather than a hand-copied literal means the two cannot drift when a tag on
// Issue changes.
func TestIssueSummaryJSONTagsMatchIssue(t *testing.T) {
	issueTags := make(map[string]string)
	issueType := reflect.TypeOf(Issue{})
	for i := 0; i < issueType.NumField(); i++ {
		f := issueType.Field(i)
		issueTags[f.Name] = f.Tag.Get("json")
	}

	summaryType := reflect.TypeOf(IssueSummary{})
	for i := 0; i < summaryType.NumField(); i++ {
		f := summaryType.Field(i)
		want, ok := issueTags[f.Name]
		if !ok {
			t.Errorf("IssueSummary.%s has no same-named field on Issue; choose its json tag deliberately and update this test", f.Name)
			continue
		}
		if got := f.Tag.Get("json"); got != want {
			t.Errorf("IssueSummary.%s json tag = %q, want %q (must match Issue.%s)", f.Name, got, want, f.Name)
		}
	}
}

// TestIssueSummaryMarshalsIssueWireKeys is the concrete counterpart to the
// reflection test above: it marshals a fully-populated summary and asserts the
// exact key set. An untagged struct would emit Go's default capitalized field
// names ("ID", "Title", …) and silently break every consumer parsing bd output,
// which is the regression this pins.
func TestIssueSummaryMarshalsIssueWireKeys(t *testing.T) {
	closedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	summary := IssueSummary{
		ID:        "bd-1",
		Title:     "narrow projection",
		Status:    StatusClosed,
		Priority:  1,
		IssueType: TypeTask,
		Assignee:  "someone",
		Pinned:    true,
		Labels:    []string{"alpha"},
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		ClosedAt:  &closedAt,
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal IssueSummary: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal IssueSummary: %v", err)
	}

	want := []string{
		"id", "title", "status", "priority", "issue_type",
		"assignee", "pinned", "labels", "created_at", "updated_at", "closed_at",
	}
	for _, key := range want {
		if _, ok := decoded[key]; !ok {
			t.Errorf("marshaled IssueSummary missing key %q; got %s", key, encoded)
		}
	}
	if len(decoded) != len(want) {
		t.Errorf("marshaled IssueSummary has %d keys, want %d; got %s", len(decoded), len(want), encoded)
	}
}

// TestIssueSummaryOmitsEmptyLikeIssue pins the omitempty half of the contract:
// Priority carries no omitempty because 0 is a valid priority (P0/critical),
// while the optional fields drop out of the document entirely when unset. A
// summary that emitted "priority" only for non-zero values would make P0 beads
// indistinguishable from unset ones in `bd list --json`.
func TestIssueSummaryOmitsEmptyLikeIssue(t *testing.T) {
	encoded, err := json.Marshal(IssueSummary{ID: "bd-1", Title: "t"})
	if err != nil {
		t.Fatalf("marshal zero-value IssueSummary: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal IssueSummary: %v", err)
	}

	for _, key := range []string{"id", "title", "priority", "created_at", "updated_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("zero-value IssueSummary should still emit %q; got %s", key, encoded)
		}
	}
	for _, key := range []string{"status", "issue_type", "assignee", "pinned", "labels", "closed_at"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("zero-value IssueSummary should omit %q; got %s", key, encoded)
		}
	}
}
