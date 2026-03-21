package notion

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestBeadsIssueFromPullIssueRoundTrip(t *testing.T) {
	t.Parallel()

	pulled := PulledIssue{
		ID:           "beads-123",
		Title:        "Sync from Notion",
		Description:  "Short summary",
		Status:       "in_progress",
		Priority:     "high",
		IssueType:    "feature",
		Assignee:     "osamu",
		Labels:       []string{"sync", "notion"},
		ExternalRef:  "https://www.notion.so/Test-0123456789abcdef0123456789abcdef",
		NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
		CreatedAt:    "2026-03-19T14:00:00Z",
		UpdatedAt:    "2026-03-19T14:05:00Z",
	}

	beadsIssue, err := BeadsIssueFromPullIssue(pulled, nil)
	if err != nil {
		t.Fatalf("BeadsIssueFromPullIssue returned error: %v", err)
	}
	if beadsIssue.Status != types.StatusInProgress {
		t.Fatalf("status = %q, want %q", beadsIssue.Status, types.StatusInProgress)
	}
	if beadsIssue.Priority != 1 {
		t.Fatalf("priority = %d, want 1", beadsIssue.Priority)
	}
	if beadsIssue.IssueType != types.TypeFeature {
		t.Fatalf("issue type = %q, want %q", beadsIssue.IssueType, types.TypeFeature)
	}
	if beadsIssue.ExternalRef == nil || *beadsIssue.ExternalRef != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("external ref = %v, want canonical notion url", beadsIssue.ExternalRef)
	}

	payload, err := PushPayloadFromIssue(beadsIssue, nil)
	if err != nil {
		t.Fatalf("PushPayloadFromIssue returned error: %v", err)
	}
	if len(payload.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(payload.Issues))
	}
	if payload.Issues[0].Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress", payload.Issues[0].Status)
	}
	if payload.Issues[0].Priority != "high" {
		t.Fatalf("priority = %q, want high", payload.Issues[0].Priority)
	}
	if payload.Issues[0].IssueType != "feature" {
		t.Fatalf("issue type = %q, want feature", payload.Issues[0].IssueType)
	}
	if len(payload.Issues[0].Labels) != 0 {
		t.Fatalf("labels = %v, want omitted for live MCP compatibility", payload.Issues[0].Labels)
	}
}

func TestBeadsIssueFromPullIssueRejectsUnsupportedEnums(t *testing.T) {
	t.Parallel()

	_, err := BeadsIssueFromPullIssue(PulledIssue{
		ID:        "beads-123",
		Title:     "Unsupported",
		Status:    "mystery",
		Priority:  "high",
		IssueType: "task",
		CreatedAt: NullableString(time.Now().Format(time.RFC3339)),
		UpdatedAt: NullableString(time.Now().Format(time.RFC3339)),
	}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported notion status") {
		t.Fatalf("error = %q, want unsupported notion status", err.Error())
	}
}

func TestPushIssueFromIssueRejectsUnsupportedExternalRef(t *testing.T) {
	t.Parallel()

	externalRef := "https://example.com/issue/123"
	_, err := PushIssueFromIssue(&types.Issue{
		ID:          "beads-123",
		Title:       "Unsupported ref",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &externalRef,
	}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported notion external ref") {
		t.Fatalf("error = %q, want unsupported notion external ref", err.Error())
	}
}

func TestPushPayloadFromIssuesWithExistingPreservesAuthoritativeSnapshots(t *testing.T) {
	t.Parallel()

	issue := &types.Issue{
		ID:        "beads-123",
		Title:     "Sync from Notion",
		Status:    types.StatusInProgress,
		Priority:  1,
		IssueType: types.TypeFeature,
	}
	pulled := PulledIssue{
		ID:           "beads-123",
		Title:        "Sync from Notion",
		Description:  "Short summary",
		Status:       "in_progress",
		Priority:     "high",
		IssueType:    "feature",
		Assignee:     "osamu",
		Labels:       []string{"sync"},
		ExternalRef:  "https://www.notion.so/Test-0123456789abcdef0123456789abcdef",
		NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
		CreatedAt:    "2026-03-19T14:00:00Z",
		UpdatedAt:    "2026-03-19T14:05:00Z",
	}

	payload, err := PushPayloadFromIssuesWithExisting([]*types.Issue{issue}, []ExistingIssue{ExistingIssueFromPullIssue(pulled)}, nil)
	if err != nil {
		t.Fatalf("PushPayloadFromIssuesWithExisting returned error: %v", err)
	}
	if len(payload.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(payload.Issues))
	}
	if len(payload.ExistingIssues) != 1 {
		t.Fatalf("existing issues = %d, want 1", len(payload.ExistingIssues))
	}
	if payload.ExistingIssues[0].NotionPageID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("page id = %q", payload.ExistingIssues[0].NotionPageID)
	}
	if payload.ExistingIssues[0].UpdatedAt != "2026-03-19T14:05:00Z" {
		t.Fatalf("updated_at = %q", payload.ExistingIssues[0].UpdatedAt)
	}
}

func TestPushPayloadFromIssuePreservesUnsupportedIssueTypeForBridge(t *testing.T) {
	t.Parallel()

	payload, err := PushPayloadFromIssue(&types.Issue{
		ID:        "beads-unsupported",
		Title:     "Custom type",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.IssueType("pm"),
	}, nil)
	if err != nil {
		t.Fatalf("PushPayloadFromIssue returned error: %v", err)
	}
	if got := payload.Issues[0].IssueType; got != "pm" {
		t.Fatalf("issue type = %q, want pm", got)
	}
}
