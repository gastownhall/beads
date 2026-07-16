package flatfile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Oracle: issueops.ClaimIssueInTx / UnclaimIssueInTx record "claimed" /
// "unclaimed" events via RecordFullEventInTable, and sqlkit's SlotSet /
// SlotClear inherit UpdateIssue's "updated" event — the flatfile audit trail
// must show the same lifecycle.

func findEvent(events []*types.Event, et types.EventType) *types.Event {
	for _, ev := range events {
		if ev.EventType == et {
			return ev
		}
	}
	return nil
}

func TestClaimUnclaimRecordEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue, "a"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.ClaimIssue(ctx, "bd-1", "worker"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}
	if err := s.UnclaimIssue(ctx, "bd-1", "worker", false); err != nil {
		t.Fatalf("UnclaimIssue: %v", err)
	}

	events, err := s.GetEvents(ctx, "bd-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}

	claimed := findEvent(events, types.EventType("claimed"))
	if claimed == nil {
		t.Fatal("no 'claimed' event recorded")
	}
	if claimed.Actor != "worker" {
		t.Errorf("claimed actor = %q, want worker", claimed.Actor)
	}
	if claimed.NewValue == nil || !strings.Contains(*claimed.NewValue, "in_progress") {
		t.Errorf("claimed new_value = %v, want assignee/in_progress payload", claimed.NewValue)
	}
	if claimed.OldValue == nil || !strings.Contains(*claimed.OldValue, `"bd-1"`) {
		t.Errorf("claimed old_value = %v, want pre-claim issue snapshot", claimed.OldValue)
	}

	unclaimed := findEvent(events, types.EventType("unclaimed"))
	if unclaimed == nil {
		t.Fatal("no 'unclaimed' event recorded")
	}
	if unclaimed.Actor != "worker" {
		t.Errorf("unclaimed actor = %q, want worker", unclaimed.Actor)
	}
	if unclaimed.NewValue == nil || !strings.Contains(*unclaimed.NewValue, `"open"`) {
		t.Errorf("unclaimed new_value = %v, want assignee/open payload", unclaimed.NewValue)
	}

	// Idempotent re-claim must not mint a second event (the reference's
	// rowsAffected==0 path returns before RecordFullEventInTable).
	if err := s.ClaimIssue(ctx, "bd-1", "worker"); err != nil {
		t.Fatalf("re-ClaimIssue: %v", err)
	}
	if err := s.ClaimIssue(ctx, "bd-1", "worker"); err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}
	events, _ = s.GetEvents(ctx, "bd-1", 0)
	n := 0
	for _, ev := range events {
		if ev.EventType == types.EventType("claimed") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("claimed events after claim/unclaim/claim/idempotent-reclaim = %d, want 2", n)
	}
}

func TestSlotSetClearRecordEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue, "a"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	countUpdated := func() int {
		events, err := s.GetEvents(ctx, "bd-1", 0)
		if err != nil {
			t.Fatalf("GetEvents: %v", err)
		}
		n := 0
		for _, ev := range events {
			if ev.EventType == types.EventUpdated {
				n++
			}
		}
		return n
	}

	if err := s.SlotSet(ctx, "bd-1", "k", "v", "actor-x"); err != nil {
		t.Fatalf("SlotSet: %v", err)
	}
	if got := countUpdated(); got != 1 {
		t.Errorf("updated events after SlotSet = %d, want 1", got)
	}
	if err := s.SlotClear(ctx, "bd-1", "k", "actor-x"); err != nil {
		t.Fatalf("SlotClear: %v", err)
	}
	if got := countUpdated(); got != 2 {
		t.Errorf("updated events after SlotClear = %d, want 2", got)
	}
	// A no-op clear records nothing (sqlkit returns before UpdateIssue).
	if err := s.SlotClear(ctx, "bd-1", "absent", "actor-x"); err != nil {
		t.Fatalf("no-op SlotClear: %v", err)
	}
	if got := countUpdated(); got != 2 {
		t.Errorf("updated events after no-op SlotClear = %d, want 2", got)
	}
}

// TestReclaimExpiredLeasesRecordsEvents extends the TASKS-5nxj audit-event
// class to the reaper path. Oracle: issueops.ReclaimExpiredLeasesInTx records
// one lease_reclaimed event per reverted issue via RecordFullEventInTable
// (actor passed through, old_value = the previous owner, new_value = "").
func TestReclaimExpiredLeasesRecordsEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, issue, "a"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.ClaimIssue(issueops.WithLeaseTTL(ctx, time.Nanosecond), "bd-1", "dead-worker"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // let the 1ns lease expire past the cutoff

	reclaimed, err := s.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "bd-1" || reclaimed[0].PreviousOwner != "dead-worker" {
		t.Fatalf("reclaimed = %+v, want [{bd-1 dead-worker}]", reclaimed)
	}

	events, err := s.GetEvents(ctx, "bd-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	ev := findEvent(events, types.EventLeaseReclaimed)
	if ev == nil {
		t.Fatal("no lease_reclaimed event recorded")
	}
	if ev.Actor != "reaper" {
		t.Errorf("lease_reclaimed actor = %q, want reaper", ev.Actor)
	}
	if ev.OldValue == nil || *ev.OldValue != "dead-worker" {
		t.Errorf("lease_reclaimed old_value = %v, want previous owner dead-worker", ev.OldValue)
	}
	if ev.NewValue == nil || *ev.NewValue != "" {
		t.Errorf("lease_reclaimed new_value = %v, want empty string", ev.NewValue)
	}
}
