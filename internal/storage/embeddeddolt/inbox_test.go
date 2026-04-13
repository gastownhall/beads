//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestInboxAddAndGet(t *testing.T) {
	env := newTestEnv(t, "inb")
	ctx := t.Context()

	item := &types.InboxItem{
		InboxID:         "test-inbox-001",
		SenderProjectID: "project-alpha",
		SenderIssueID:   "alpha-42",
		Title:           "Bug in shared module",
		Description:     "The shared auth module panics on nil user",
		Priority:        1,
		IssueType:       "bug",
		SenderRef:       "https://project-alpha.example/issues/42",
		Metadata:        `{"labels":["critical"]}`,
	}

	if err := env.store.AddInboxItem(ctx, item); err != nil {
		t.Fatalf("AddInboxItem: %v", err)
	}

	got, err := env.store.GetInboxItem(ctx, "test-inbox-001")
	if err != nil {
		t.Fatalf("GetInboxItem: %v", err)
	}

	if got.Title != item.Title {
		t.Errorf("Title = %q, want %q", got.Title, item.Title)
	}
	if got.SenderProjectID != item.SenderProjectID {
		t.Errorf("SenderProjectID = %q, want %q", got.SenderProjectID, item.SenderProjectID)
	}
	if got.Priority != item.Priority {
		t.Errorf("Priority = %d, want %d", got.Priority, item.Priority)
	}
	if got.IssueType != item.IssueType {
		t.Errorf("IssueType = %q, want %q", got.IssueType, item.IssueType)
	}
	if got.ImportedAt != nil {
		t.Errorf("ImportedAt should be nil for new item, got %v", got.ImportedAt)
	}
}

func TestInboxIdempotentResend(t *testing.T) {
	env := newTestEnv(t, "idm")
	ctx := t.Context()

	item := &types.InboxItem{
		InboxID:         "resend-001",
		SenderProjectID: "proj-a",
		SenderIssueID:   "a-10",
		Title:           "Original title",
		Description:     "Original description",
		Priority:        2,
		IssueType:       "task",
	}

	if err := env.store.AddInboxItem(ctx, item); err != nil {
		t.Fatalf("AddInboxItem (first): %v", err)
	}

	// Resend with updated title — should update via UNIQUE(sender_project_id, sender_issue_id)
	item.InboxID = "resend-002"
	item.Title = "Updated title"
	if err := env.store.AddInboxItem(ctx, item); err != nil {
		t.Fatalf("AddInboxItem (resend): %v", err)
	}

	// Count pending — should be 1, not 2 (dedup by sender_project_id + sender_issue_id)
	count, err := env.store.CountPendingInbox(ctx)
	if err != nil {
		t.Fatalf("CountPendingInbox: %v", err)
	}
	if count != 1 {
		t.Errorf("CountPendingInbox = %d, want 1 (idempotent resend should not duplicate)", count)
	}

	// Verify the title was updated (ON DUPLICATE KEY UPDATE worked)
	got, err := env.store.GetInboxItem(ctx, "resend-001")
	if err != nil {
		t.Fatalf("GetInboxItem after resend: %v", err)
	}
	if got.Title != "Updated title" {
		t.Errorf("Title after resend = %q, want %q", got.Title, "Updated title")
	}
}

func TestInboxPendingFiltering(t *testing.T) {
	env := newTestEnv(t, "pnd")
	ctx := t.Context()

	// Add 3 items
	for i, id := range []string{"p-001", "p-002", "p-003"} {
		item := &types.InboxItem{
			InboxID:         id,
			SenderProjectID: "sender",
			SenderIssueID:   id,
			Title:           "Issue " + id,
			Priority:        i,
			IssueType:       "task",
		}
		if err := env.store.AddInboxItem(ctx, item); err != nil {
			t.Fatalf("AddInboxItem(%s): %v", id, err)
		}
	}

	// All 3 should be pending
	pending, err := env.store.GetPendingInboxItems(ctx)
	if err != nil {
		t.Fatalf("GetPendingInboxItems: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("GetPendingInboxItems len = %d, want 3", len(pending))
	}

	// Import one
	if err := env.store.MarkInboxItemImported(ctx, "p-001", "local-issue-1"); err != nil {
		t.Fatalf("MarkInboxItemImported: %v", err)
	}

	// Reject one
	if err := env.store.MarkInboxItemRejected(ctx, "p-002", "not relevant"); err != nil {
		t.Fatalf("MarkInboxItemRejected: %v", err)
	}

	// Only 1 should be pending now
	pending, err = env.store.GetPendingInboxItems(ctx)
	if err != nil {
		t.Fatalf("GetPendingInboxItems after mark: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("GetPendingInboxItems len = %d, want 1", len(pending))
	}
	if pending[0].InboxID != "p-003" {
		t.Errorf("remaining pending ID = %q, want %q", pending[0].InboxID, "p-003")
	}

	// Verify imported item
	imported, err := env.store.GetInboxItem(ctx, "p-001")
	if err != nil {
		t.Fatalf("GetInboxItem(p-001): %v", err)
	}
	if imported.ImportedAt == nil {
		t.Error("imported item should have ImportedAt set")
	}
	if imported.ImportedIssueID != "local-issue-1" {
		t.Errorf("ImportedIssueID = %q, want %q", imported.ImportedIssueID, "local-issue-1")
	}

	// Verify rejected item
	rejected, err := env.store.GetInboxItem(ctx, "p-002")
	if err != nil {
		t.Fatalf("GetInboxItem(p-002): %v", err)
	}
	if rejected.RejectedAt == nil {
		t.Error("rejected item should have RejectedAt set")
	}
	if rejected.RejectionReason != "not relevant" {
		t.Errorf("RejectionReason = %q, want %q", rejected.RejectionReason, "not relevant")
	}
}

func TestInboxClean(t *testing.T) {
	env := newTestEnv(t, "cln")
	ctx := t.Context()

	// Add items in various states
	items := []struct {
		id     string
		action string // "import", "reject", or "pending"
	}{
		{"c-001", "import"},
		{"c-002", "reject"},
		{"c-003", "pending"},
	}

	for _, it := range items {
		item := &types.InboxItem{
			InboxID:         it.id,
			SenderProjectID: "sender",
			SenderIssueID:   it.id,
			Title:           "Issue " + it.id,
			Priority:        2,
			IssueType:       "task",
		}
		if err := env.store.AddInboxItem(ctx, item); err != nil {
			t.Fatalf("AddInboxItem(%s): %v", it.id, err)
		}
		switch it.action {
		case "import":
			if err := env.store.MarkInboxItemImported(ctx, it.id, "local-"+it.id); err != nil {
				t.Fatalf("MarkImported(%s): %v", it.id, err)
			}
		case "reject":
			if err := env.store.MarkInboxItemRejected(ctx, it.id, "reason"); err != nil {
				t.Fatalf("MarkRejected(%s): %v", it.id, err)
			}
		}
	}

	removed, err := env.store.CleanInbox(ctx)
	if err != nil {
		t.Fatalf("CleanInbox: %v", err)
	}
	if removed != 2 {
		t.Errorf("CleanInbox removed %d, want 2 (imported + rejected)", removed)
	}

	// Pending item should still exist
	pending, err := env.store.GetPendingInboxItems(ctx)
	if err != nil {
		t.Fatalf("GetPendingInboxItems: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("remaining pending = %d, want 1", len(pending))
	}
}

func TestInboxCountPending(t *testing.T) {
	env := newTestEnv(t, "cnt")
	ctx := t.Context()

	count, err := env.store.CountPendingInbox(ctx)
	if err != nil {
		t.Fatalf("CountPendingInbox (empty): %v", err)
	}
	if count != 0 {
		t.Errorf("CountPendingInbox (empty) = %d, want 0", count)
	}

	for _, id := range []string{"cnt-1", "cnt-2"} {
		item := &types.InboxItem{
			InboxID:         id,
			SenderProjectID: "sender",
			SenderIssueID:   id,
			Title:           "Issue " + id,
			Priority:        2,
			IssueType:       "task",
		}
		if err := env.store.AddInboxItem(ctx, item); err != nil {
			t.Fatalf("AddInboxItem(%s): %v", id, err)
		}
	}

	count, err = env.store.CountPendingInbox(ctx)
	if err != nil {
		t.Fatalf("CountPendingInbox: %v", err)
	}
	if count != 2 {
		t.Errorf("CountPendingInbox = %d, want 2", count)
	}
}

// TestInboxExpiryFiltering tests that expired items are excluded from pending.
// Note: This test uses a direct SQL insert to set expires_at in the past,
// since the AddInboxItem API uses the InboxItem struct's ExpiresAt field.
func TestInboxExpiryFiltering(t *testing.T) {
	env := newTestEnv(t, "exp")
	ctx := t.Context()

	// Add a normal item
	normal := &types.InboxItem{
		InboxID:         "exp-normal",
		SenderProjectID: "sender",
		SenderIssueID:   "normal",
		Title:           "Normal item",
		Priority:        2,
		IssueType:       "task",
	}
	if err := env.store.AddInboxItem(ctx, normal); err != nil {
		t.Fatalf("AddInboxItem(normal): %v", err)
	}

	// Add an expired item with ExpiresAt in the past
	expired := &types.InboxItem{
		InboxID:         "exp-expired",
		SenderProjectID: "sender",
		SenderIssueID:   "expired",
		Title:           "Expired item",
		Priority:        2,
		IssueType:       "task",
		ExpiresAt:       timePtr(time.Now().Add(-1 * time.Hour)),
	}
	if err := env.store.AddInboxItem(ctx, expired); err != nil {
		t.Fatalf("AddInboxItem(expired): %v", err)
	}

	// Only the normal item should be pending
	pending, err := env.store.GetPendingInboxItems(ctx)
	if err != nil {
		t.Fatalf("GetPendingInboxItems: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("GetPendingInboxItems len = %d, want 1 (expired item should be excluded)", len(pending))
	}

	// Clean should remove expired item
	removed, err := env.store.CleanInbox(ctx)
	if err != nil {
		t.Fatalf("CleanInbox: %v", err)
	}
	if removed != 1 {
		t.Errorf("CleanInbox removed %d, want 1 (expired item)", removed)
	}
}

func TestInboxSizeValidation(t *testing.T) {
	env := newTestEnv(t, "inbsz")
	ctx := t.Context()

	// Title exceeding max should fail
	item := &types.InboxItem{
		InboxID:         "test-size-001",
		SenderProjectID: "proj-a",
		SenderIssueID:   "a-1",
		Title:           string(make([]byte, 501)),
		Priority:        2,
		IssueType:       "task",
	}
	if err := env.store.AddInboxItem(ctx, item); err == nil {
		t.Fatal("expected error for oversized title")
	}

	// Description exceeding max should fail
	item.Title = "Normal title"
	item.Description = string(make([]byte, 100_001))
	if err := env.store.AddInboxItem(ctx, item); err == nil {
		t.Fatal("expected error for oversized description")
	}

	// Metadata exceeding max should fail
	item.Description = "Normal desc"
	item.Metadata = string(make([]byte, 50_001))
	if err := env.store.AddInboxItem(ctx, item); err == nil {
		t.Fatal("expected error for oversized metadata")
	}

	// Valid sizes should succeed
	item.Metadata = "{}"
	if err := env.store.AddInboxItem(ctx, item); err != nil {
		t.Fatalf("valid item should succeed: %v", err)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
