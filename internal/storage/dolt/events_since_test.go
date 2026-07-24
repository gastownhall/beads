package dolt

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsSince verifies the durable keyset event read: (created_at, id)
// ordering, same-second id tie-break, cursor exclusivity, limit clamping, and
// durable-only scope (wisp events excluded).
func TestEventsSince(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// A durable issue to anchor events (events.issue_id → issues.id FK).
	durable := &types.Issue{ID: "es-durable", Title: "Durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("create durable issue: %v", err)
	}
	// A wisp issue whose "created" event lands in wisp_events, which the
	// durable-only feed must never surface.
	wisp := &types.Issue{ID: "es-wisp", Title: "Wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp issue: %v", err)
	}

	// Clear the auto-generated "created" event so the seeded rows are the only
	// durable events, giving a fully deterministic ordering.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM events WHERE issue_id = ?", durable.ID); err != nil {
		t.Fatalf("clear auto events: %v", err)
	}

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	type seed struct {
		id string
		at time.Time
	}
	// e1 and e2 share a second (tie broken by id ASC); e3 is one second later.
	seeds := []seed{
		{id: "00000000-0000-0000-0000-000000000001", at: base},
		{id: "00000000-0000-0000-0000-000000000002", at: base},
		{id: "00000000-0000-0000-0000-000000000003", at: base.Add(time.Second)},
	}
	for _, s := range seeds {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES (?, ?, ?, ?, ?)",
			s.id, durable.ID, string(types.EventUpdated), "tester", s.at); err != nil {
			t.Fatalf("insert seed event %s: %v", s.id, err)
		}
	}

	ids := func(evs []*types.Event) []string {
		out := make([]string, len(evs))
		for i, e := range evs {
			out[i] = e.ID
		}
		return out
	}
	eq := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("event ids = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("event ids = %v, want %v", got, want)
			}
		}
	}

	// Empty cursor = from epoch; ordered by (created_at ASC, id ASC), durable only.
	all, err := store.EventsSince(ctx, storage.EventCursor{}, "", 10)
	if err != nil {
		t.Fatalf("EventsSince(epoch): %v", err)
	}
	eq(t, ids(all), []string{seeds[0].id, seeds[1].id, seeds[2].id})
	for _, e := range all {
		if e.IssueID == wisp.ID {
			t.Fatalf("durable-only feed returned wisp event for %s", wisp.ID)
		}
	}

	// Limit honored.
	page, err := store.EventsSince(ctx, storage.EventCursor{}, "", 2)
	if err != nil {
		t.Fatalf("EventsSince(limit=2): %v", err)
	}
	eq(t, ids(page), []string{seeds[0].id, seeds[1].id})

	// Cursor excludes its own row and orders the same-second tie by id: starting
	// at e1's (created_at, id) yields e2 then e3.
	afterE1, err := store.EventsSince(ctx, storage.EventCursor{CreatedAt: seeds[0].at, ID: seeds[0].id}, "", 10)
	if err != nil {
		t.Fatalf("EventsSince(after e1): %v", err)
	}
	eq(t, ids(afterE1), []string{seeds[1].id, seeds[2].id})

	// Exhausting the shared second advances to the next second's row.
	afterE2, err := store.EventsSince(ctx, storage.EventCursor{CreatedAt: seeds[1].at, ID: seeds[1].id}, "", 10)
	if err != nil {
		t.Fatalf("EventsSince(after e2): %v", err)
	}
	eq(t, ids(afterE2), []string{seeds[2].id})
}

// TestEventsSinceClaimedConstant verifies the real claim path writes an event
// whose type is the extracted EventClaimed constant ("claimed"), reachable
// through the durable keyset read.
func TestEventsSinceClaimedConstant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "es-claim", Title: "Claimable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.ClaimIssue(ctx, issue.ID, "worker"); err != nil {
		t.Fatalf("claim issue: %v", err)
	}

	evs, err := store.EventsSince(ctx, storage.EventCursor{}, "", 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.IssueID == issue.ID && e.EventType == types.EventClaimed {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q event found for claimed issue %s", types.EventClaimed, issue.ID)
	}
}

// TestEventsSinceIssueFilter verifies the optional per-bead scope: a non-empty
// issueID restricts the durable feed to that issue's events, while "" returns
// every issue's events. This is the primitive behind `bd show`'s per-bead
// event history.
func TestEventsSinceIssueFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, err := store.db.ExecContext(ctx, "DELETE FROM events WHERE issue_id = ?", id); err != nil {
			t.Fatalf("clear auto events for %s: %v", id, err)
		}
	}
	mk("ef-a")
	mk("ef-b")

	base := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := func(id, issueID string, at time.Time) {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES (?, ?, ?, ?, ?)",
			id, issueID, string(types.EventUpdated), "tester", at); err != nil {
			t.Fatalf("insert seed event %s: %v", id, err)
		}
	}
	seed("00000000-0000-0000-0000-0000000000a1", "ef-a", base)
	seed("00000000-0000-0000-0000-0000000000a2", "ef-a", base.Add(time.Second))
	seed("00000000-0000-0000-0000-0000000000b1", "ef-b", base.Add(2*time.Second))

	issueIDsOf := func(evs []*types.Event) map[string]int {
		m := map[string]int{}
		for _, e := range evs {
			m[e.IssueID]++
		}
		return m
	}

	onlyA, err := store.EventsSince(ctx, storage.EventCursor{}, "ef-a", 100)
	if err != nil {
		t.Fatalf("EventsSince(issue=ef-a): %v", err)
	}
	if got := issueIDsOf(onlyA); got["ef-a"] != 2 || got["ef-b"] != 0 {
		t.Fatalf("filtered ef-a feed = %v, want {ef-a:2}", got)
	}

	onlyB, err := store.EventsSince(ctx, storage.EventCursor{}, "ef-b", 100)
	if err != nil {
		t.Fatalf("EventsSince(issue=ef-b): %v", err)
	}
	if got := issueIDsOf(onlyB); got["ef-b"] != 1 || got["ef-a"] != 0 {
		t.Fatalf("filtered ef-b feed = %v, want {ef-b:1}", got)
	}

	unfiltered, err := store.EventsSince(ctx, storage.EventCursor{}, "", 100)
	if err != nil {
		t.Fatalf("EventsSince(all): %v", err)
	}
	if got := issueIDsOf(unfiltered); got["ef-a"] != 2 || got["ef-b"] != 1 {
		t.Fatalf("unfiltered feed = %v, want {ef-a:2, ef-b:1}", got)
	}
}

// TestEventsSinceLimitClamp verifies the self-clamp: limit <= 0 falls back to
// the store default (100) and any limit above the hard cap is clamped to 500.
func TestEventsSinceLimitClamp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "clamp", Title: "clamp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM events WHERE issue_id = ?", iss.ID); err != nil {
		t.Fatalf("clear auto events: %v", err)
	}

	// Seed 501 durable events in one multi-row insert (cheaper than 501 round
	// trips). ids sort lexically in numeric order, so (created_at, id) ordering
	// is the seed order.
	const n = 501
	base := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteString("INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES ")
	args := make([]any, 0, n*5)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("(?, ?, ?, ?, ?)")
		args = append(args,
			fmt.Sprintf("clamp-%05d", i), iss.ID, string(types.EventUpdated), "tester", base.Add(time.Duration(i)*time.Second))
	}
	if _, err := store.db.ExecContext(ctx, b.String(), args...); err != nil {
		t.Fatalf("bulk insert seed events: %v", err)
	}

	// limit <= 0 => issueops.defaultEventsSinceLimit (100).
	const wantDefault, wantCap = 100, 500
	def, err := store.EventsSince(ctx, storage.EventCursor{}, "", 0)
	if err != nil {
		t.Fatalf("EventsSince(limit=0): %v", err)
	}
	if len(def) != wantDefault {
		t.Fatalf("default clamp: got %d rows, want %d", len(def), wantDefault)
	}

	// limit above the cap => issueops.maxEventsSinceLimit (500).
	capped, err := store.EventsSince(ctx, storage.EventCursor{}, "", 100000)
	if err != nil {
		t.Fatalf("EventsSince(limit=100000): %v", err)
	}
	if len(capped) != wantCap {
		t.Fatalf("cap clamp: got %d rows, want %d", len(capped), wantCap)
	}
}

// TestEventsSincePlanIsIndexed is the sargability regression guard: the
// EventsSince cursor predicate must seek idx_events_created_at
// (IndexedTableAccess), not full-scan-and-filter. The redundant `created_at >= ?`
// lower bound is what flips the Dolt planner from Table+Filter+TopN to an
// indexed range. It EXPLAINs the exact predicate shape with literals and skips
// (rather than fails) if the EXPLAIN output format is unrecognizable.
func TestEventsSincePlanIsIndexed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "plan", Title: "plan", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES (?, ?, ?, ?, ?)",
			fmt.Sprintf("plan-%02d", i), iss.ID, string(types.EventUpdated), "tester", base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("insert seed event: %v", err)
		}
	}

	const cur = "2023-01-01 00:00:00"
	// Single-source the guarded SQL from production: EXPLAIN the exact string
	// EventsSinceInTx runs (issueID="" => three ? placeholders: the created_at
	// lower bound, the strict created_at, and the id tie-break), with the
	// placeholders replaced by literals for the plan. A change to the SARGABLE
	// predicate in issueops.EventsSinceQuery then breaks this guard.
	prod := issueops.EventsSinceQuery("", 100)
	plan := explainPlan(t, ctx, store.db, literalizeParams(prod, "'"+cur+"'", "'"+cur+"'", "''"))

	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, "events.created_at") {
		t.Fatalf("EventsSince predicate does not seek idx_events_created_at (want IndexedTableAccess on [events.created_at]) — the sargable lower bound regressed to a full Table scan.\nplan:\n%s", plan)
	}
}
