package flatfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// SQL reference semantics: an event row of any size is stored and returned
// intact. The flatfile reader must not impose a line-length cap (bufio.Scanner
// defaults to 64KB) on events embedding large issue snapshots.
func TestGetEventsLargeEvent(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "big-1", Title: "Big"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	big := strings.Repeat("x", 200*1024) // well past Scanner's 64KB default
	if err := s.recordEvent("big-1", types.EventUpdated, "tester", big, "new"); err != nil {
		t.Fatalf("recordEvent: %v", err)
	}

	events, err := s.GetEvents(ctx, "big-1", 0)
	if err != nil {
		t.Fatalf("GetEvents after >64KB event: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.OldValue != nil && *ev.OldValue == big {
			found = true
		}
	}
	if !found {
		t.Error("large event not returned intact")
	}

	if _, err := s.GetAllEventsSince(ctx, time.Time{}); err != nil {
		t.Errorf("GetAllEventsSince after >64KB event: %v", err)
	}
	if n, err := s.CountEvents(ctx, "big-1", 0); err != nil || n != int64(len(events)) {
		t.Errorf("CountEvents = %d, %v; want %d, nil", n, err, len(events))
	}
}

// SQL reference semantics: one bad row cannot make every other event row
// unreadable. A torn append (crash mid-write, no trailing newline) must not
// brick GetEvents/GetAllEventsSince, and the next append must not merge into
// the torn fragment.
func TestReadEventsTolerantOfTornLine(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "torn-1", Title: "Torn"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	before, err := s.GetEvents(ctx, "torn-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}

	// Simulate a crash mid-append: partial JSON, no trailing newline.
	path := s.eventFilename("torn-1")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	if _, err := f.WriteString(`{"id":"torn","issue_id":"torn-1","event_ty`); err != nil {
		t.Fatalf("write torn fragment: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The next event must survive the torn fragment.
	if err := s.recordEvent("torn-1", types.EventUpdated, "tester", "old", "new"); err != nil {
		t.Fatalf("recordEvent after torn line: %v", err)
	}

	after, err := s.GetEvents(ctx, "torn-1", 0)
	if err != nil {
		t.Fatalf("GetEvents after torn line: %v", err)
	}
	if want := len(before) + 1; len(after) != want {
		t.Errorf("got %d events, want %d (torn fragment skipped, new event kept)", len(after), want)
	}
	if _, err := s.GetAllEventsSince(ctx, time.Time{}); err != nil {
		t.Errorf("GetAllEventsSince with torn line: %v", err)
	}
}

// SQL reference semantics: the events query never sees stray filesystem
// artifacts. A dotfile like macOS AppleDouble "._TASKS-1.jsonl" in the events
// dir must not fail the store-wide scan.
func TestGetAllEventsSinceSkipsStrayDotfiles(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "real-1", Title: "Real"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	stray := filepath.Join(s.eventsDir, "._real-1.jsonl")
	if err := os.WriteFile(stray, []byte("\x00\x05\x16\x07AppleDouble junk"), 0o644); err != nil {
		t.Fatalf("write stray dotfile: %v", err)
	}

	events, err := s.GetAllEventsSince(ctx, time.Time{})
	if err != nil {
		t.Fatalf("GetAllEventsSince with stray dotfile: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.IssueID == "real-1" && ev.EventType == types.EventCreated {
			found = true
		}
	}
	if !found {
		t.Error("real issue's created event missing from scan")
	}
}

// SQL reference semantics: CountEvents on both sqlkit and dolt is a bare
// `SELECT count(*) FROM events` — durable table only, never wisp-routed — so
// a wisp issue always counts 0 even though GetEvents (which IS wisp-routed)
// returns its events. Flatfile stores both kinds in one JSONL file and must
// replicate the asymmetry, as CountIssueComments already does.
func TestCountEventsWispReportsZero(t *testing.T) {
	s := newTestStore(t)

	wisp := &types.Issue{ID: "wispcnt-1", Title: "Wisp", Priority: 2, Ephemeral: true}
	if err := s.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.recordEvent("wispcnt-1", types.EventUpdated, "tester", "old", "new"); err != nil {
		t.Fatalf("recordEvent: %v", err)
	}

	events, err := s.GetEvents(ctx, "wispcnt-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("wisp has no events; test needs at least one to prove the count is zeroed")
	}

	n, err := s.CountEvents(ctx, "wispcnt-1", 0)
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if n != 0 {
		t.Errorf("CountEvents(wisp) = %d, want 0: SQL backends count only the durable events table", n)
	}

	// Durable issues keep their real count.
	durable := &types.Issue{ID: "durcnt-1", Title: "Durable", Priority: 2}
	if err := s.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("CreateIssue durable: %v", err)
	}
	dn, err := s.CountEvents(ctx, "durcnt-1", 0)
	if err != nil {
		t.Fatalf("CountEvents durable: %v", err)
	}
	devents, err := s.GetEvents(ctx, "durcnt-1", 0)
	if err != nil {
		t.Fatalf("GetEvents durable: %v", err)
	}
	if dn == 0 || dn != int64(len(devents)) {
		t.Errorf("CountEvents(durable) = %d, want %d (nonzero)", dn, len(devents))
	}
}

// Audit-order semantics: JSONL append order is true insertion order, so
// events sharing a CreatedAt (coarse clocks, created+label bursts) must come
// back in insertion order deterministically, not scrambled by an unstable
// sort.
func TestEventOrderStableForEqualTimestamps(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "tie-1", Title: "Tie"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ids := []string{"ev-a", "ev-b", "ev-c", "ev-d", "ev-e", "ev-f", "ev-g", "ev-h"}
	for _, id := range ids {
		ev := &types.Event{ID: id, IssueID: "tie-1", EventType: types.EventUpdated, Actor: "tester", CreatedAt: ts}
		if err := s.appendEvent(ev); err != nil {
			t.Fatalf("appendEvent %s: %v", id, err)
		}
	}

	events, err := s.GetEvents(ctx, "tie-1", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	var gotDesc []string
	for _, ev := range events {
		if ev.CreatedAt.Equal(ts) {
			gotDesc = append(gotDesc, ev.ID)
		}
	}
	for i, id := range ids {
		if i >= len(gotDesc) || gotDesc[i] != id {
			t.Fatalf("GetEvents equal-timestamp order = %v, want insertion order %v", gotDesc, ids)
		}
	}

	all, err := s.GetAllEventsSince(ctx, ts.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetAllEventsSince: %v", err)
	}
	var gotAsc []string
	for _, ev := range all {
		if ev.CreatedAt.Equal(ts) {
			gotAsc = append(gotAsc, ev.ID)
		}
	}
	for i, id := range ids {
		if i >= len(gotAsc) || gotAsc[i] != id {
			t.Fatalf("GetAllEventsSince equal-timestamp order = %v, want insertion order %v", gotAsc, ids)
		}
	}
}
