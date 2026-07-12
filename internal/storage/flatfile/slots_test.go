package flatfile

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Oracle: sqlkit.SlotClear (internal/storage/sqlkit/slots.go) — a corrupt
// metadata blob is an error, and clearing an absent key returns before the
// UPDATE, so updated_at is untouched.

func TestSlotClearAbsentKeyDoesNotBumpUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue, "a"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.SlotSet(ctx, "bd-1", "present", "v", "a"); err != nil {
		t.Fatalf("SlotSet: %v", err)
	}
	before, err := s.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // ensure a rewrite would move updated_at
	if err := s.SlotClear(ctx, "bd-1", "absent", "a"); err != nil {
		t.Fatalf("SlotClear absent key: %v", err)
	}
	after, err := s.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updated_at bumped by no-op clear: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
	if v, err := s.SlotGet(ctx, "bd-1", "present"); err != nil || v != "v" {
		t.Errorf("SlotGet present = (%q, %v), want (v, nil)", v, err)
	}
}

func TestSlotClearCorruptMetadataErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue, "a"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	// Valid JSON, but not an object: unmarshal into map[string]interface{}
	// fails, exactly the condition sqlkit surfaces as 'parsing metadata'.
	stored, err := s.readIssue("bd-1")
	if err != nil {
		t.Fatalf("readIssue: %v", err)
	}
	stored.Metadata = json.RawMessage(`"not-an-object"`)
	if err := s.writeIssue(stored); err != nil {
		t.Fatalf("writeIssue: %v", err)
	}

	if err := s.SlotClear(ctx, "bd-1", "k", "a"); err == nil {
		t.Error("SlotClear on corrupt metadata: want error, got nil")
	}
}
