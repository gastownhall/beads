package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// goldenPath is the committed consumer contract: one journal line per record, in
// the exact shape `bd events tail`/`export` emit. Regenerate with
// BD_UPDATE_GOLDEN=1 go test ./cmd/bd/ -run TestEventsJournalGolden.
const goldenPath = "testdata/events_journal_records.jsonl"

// TestEventsJournalGolden pins the external record contract for the durable
// events journal. It marshals REAL beads types.Issue, EventDep and EventComment
// values through the same eventsjournal.NewRecord projection `bd events tail`
// and GET /v0/beads/events both serve from, so the golden
// captures bd's actual field marshaling — issue_type, omitempty elision, the
// top-level dep edge (kind/target/metadata), and the replayable comment payload
// — that external consumers parse. A change to the wire shape (a renamed/added/
// removed field, a lost omitempty) fails this test until the golden is
// regenerated deliberately.
func TestEventsJournalGolden(t *testing.T) {
	got := renderGoldenLines(t)

	// The runtime snapshot comes from issueops.getJournalIssueInTx, which loads
	// the issue row and its labels only — never its Dependencies. So no journal
	// record can carry an inline "dependencies" array; dependency edges surface
	// solely through the top-level "dep" field on dep_add / dep_remove records.
	// Enforce that here so the golden pins what bd actually writes.
	assertNoInlineDependencies(t, got)
	assertCommentSourcesAreEmittable(t, got)

	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with BD_UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("journal record contract drifted from %s.\n--- got ---\n%s\n--- want ---\n%s\nregenerate with BD_UPDATE_GOLDEN=1 if intended", goldenPath, got, want)
	}
}

// TestEventsJournalGoldenIsDeterministic renders the fixture twice and requires
// byte equality.
//
// It exists because an issue snapshot carries wall-clock ride-alongs — most
// notably the lease pair lease_expires_at / heartbeat_at, which a claim stamps
// from time.Now(). A fixture that reached for the clock (directly, or by
// building an issue through a claim helper) would still pass TestEventsJournal
// Golden on the run that regenerated it and fail on every run afterwards, which
// reads as flake rather than as the fixture bug it is. Every fixture below is
// therefore constructed with FIXED timestamps, including the lease pair on the
// claimed issue: the golden pins the shape a consumer of a claimed bead really
// sees rather than pretending leases do not exist, and stays reproducible.
func TestEventsJournalGoldenIsDeterministic(t *testing.T) {
	if !bytes.Equal(renderGoldenLines(t), renderGoldenLines(t)) {
		t.Error("golden fixture is not reproducible: something in renderGoldenLines reads the clock or iterates a map")
	}
}

// renderGoldenLines builds the fixture records and returns them as JSONL exactly
// as eventsjournal.NewRecord + the tail encoder would emit them.
func renderGoldenLines(t *testing.T) []byte {
	t.Helper()
	ts := "2026-01-02T03:04:05Z" // normalized UTC insert time, as the read seam yields
	created := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closed := time.Date(2026, 1, 2, 3, 30, 0, 0, time.UTC)
	heartbeat := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	leaseExpiry := time.Date(2026, 1, 2, 4, 4, 0, 0, time.UTC)
	est := 90

	// A minimal open task — the common case; exercises omitempty elision.
	minimal := &types.Issue{
		ID: "bd-100", Title: "wire the seam", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 1, CreatedAt: created, UpdatedAt: updated,
	}
	// A richly populated feature with labels, metadata, and an external ref —
	// exercises the full field surface consumers may read. It deliberately omits
	// Dependencies: the runtime snapshot loads the issue row and its labels
	// only, so a real journal record never carries an inline "dependencies"
	// array. Dependency edges are recorded solely through the top-level "dep"
	// field on the dep_add / dep_remove records below.
	full := &types.Issue{
		ID: "bd-101", Title: "durable journal", Description: "append-only record",
		AcceptanceCriteria: "replayable", Status: types.StatusInProgress,
		IssueType: types.TypeFeature, Priority: 0, Assignee: "worker-1",
		Owner: "dev@example.com", EstimatedMinutes: &est, CreatedAt: created,
		CreatedBy: "author", UpdatedAt: updated, ExternalRef: strptr("gh-9"),
		SourceSystem: "github", Metadata: json.RawMessage(`{"k":"v"}`),
		Labels: []string{"infra", "urgent"},
	}
	// A claimed issue, carrying the lease pair a claim stamps. Fixed values, not
	// time.Now(): see TestEventsJournalGoldenIsDeterministic.
	claimed := &types.Issue{
		ID: "bd-102", Title: "leased work", Status: types.StatusInProgress,
		IssueType: types.TypeTask, Priority: 1, Assignee: "worker-1",
		CreatedAt: created, UpdatedAt: updated,
		LeaseExpiresAt: &leaseExpiry, HeartbeatAt: &heartbeat,
	}
	// A closed issue — exercises close_reason / closed_at marshaling.
	closedIssue := &types.Issue{
		ID: "bd-101", Title: "durable journal", Status: types.StatusClosed,
		IssueType: types.TypeFeature, Priority: 0, CreatedAt: created,
		UpdatedAt: closed, ClosedAt: &closed, CloseReason: "shipped",
	}
	// A blocked issue — is_blocked is the persisted readiness projection a graph
	// delta needs to be replayable, and it is journal-only (omitempty, so the
	// false case is elided).
	blocked := &types.Issue{
		ID: "bd-100", Title: "wire the seam", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 1, CreatedAt: created, UpdatedAt: updated,
		IsBlocked: true,
	}
	// An ephemeral wisp — exercises the ephemeral/wisp_type fields.
	wisp := &types.Issue{
		ID: "bd-wisp-1", Title: "convoy member", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 2, CreatedAt: created, UpdatedAt: updated,
		Ephemeral: true, WispType: types.WispType("convoy"),
	}
	// The engine-only comment op: a replayable comment payload, so a consumer
	// can reproduce comment text without re-reading the database. Both sources
	// the engine can emit are pinned, and they are named by the CONSTANTS the
	// emit sites use — a fixture cannot invent a source value that no emitter
	// produces, which is what the earlier hand-written "comment" was.
	structuredComment := &issueops.EventComment{
		ID: "cmt-1", Author: "worker-1", Text: "picked this up",
		CreatedAt: updated, Source: issueops.CommentSourceStructured,
	}
	auditComment := &issueops.EventComment{
		ID: "cmt-2", Author: "worker-1", Text: "status note",
		CreatedAt: updated, Source: issueops.CommentSourceAudit,
	}

	// actor pins both halves of the attribution contract: an attributed row
	// emits the acting identity (the same one the audit-events table resolves),
	// and an actorless row — derived maintenance, actorless delete plumbing,
	// records from before the column existed — omits the member entirely.
	records := []eventsjournal.Record{
		goldenRecord(1, ts, string(issueops.EventCreate), minimal.ID, "author", mustJSON(t, minimal), "", ""),
		goldenRecord(2, ts, string(issueops.EventCreate), full.ID, "author", mustJSON(t, full), "", ""),
		goldenRecord(3, ts, string(issueops.EventDepAdd), "bd-101", "author", mustJSON(t, full),
			mustJSON(t, &issueops.EventDep{Kind: string(types.DepBlocks), Target: "bd-100"}), ""),
		// A derived-maintenance update (is_blocked recompute) carries no actor.
		goldenRecord(4, ts, string(issueops.EventUpdate), blocked.ID, "", mustJSON(t, blocked), "", ""),
		goldenRecord(5, ts, string(issueops.EventDepRemove), "bd-101", "author", mustJSON(t, full),
			mustJSON(t, &issueops.EventDep{Kind: string(types.DepBlocks), Target: "bd-100", Metadata: `{"note":"unblocked"}`}), ""),
		goldenRecord(6, ts, string(issueops.EventUpdate), claimed.ID, "worker-1", mustJSON(t, claimed), "", ""),
		goldenRecord(7, ts, string(issueops.EventCommentWrite), claimed.ID, "worker-1", mustJSON(t, claimed), "", mustJSON(t, structuredComment)),
		goldenRecord(8, ts, string(issueops.EventCommentWrite), claimed.ID, "worker-1", mustJSON(t, claimed), "", mustJSON(t, auditComment)),
		goldenRecord(9, ts, string(issueops.EventClose), closedIssue.ID, "worker-1", mustJSON(t, closedIssue), "", ""),
		goldenRecord(10, ts, string(issueops.EventCreate), wisp.ID, "author", mustJSON(t, wisp), "", ""),
		goldenRecord(11, ts, string(issueops.EventDelete), "bd-100", "", "", "", ""), // null issue on delete; actorless delete plumbing
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode record: %v", err)
		}
	}
	return buf.Bytes()
}

// goldenRecord spells one stored row positionally, so the fixture below reads
// as a table. The projection it runs through is the shipped one — the fixture
// must not be able to construct a record shape no reader can produce.
func goldenRecord(seq int64, ts, op, issueID, actor, issueJS, depJS, commentJS string) eventsjournal.Record {
	return eventsjournal.NewRecord(storage.EventsJournalRow{
		Seq:         seq,
		TS:          ts,
		Op:          op,
		IssueID:     issueID,
		Actor:       actor,
		IssueJSON:   issueJS,
		DepJSON:     depJS,
		CommentJSON: commentJS,
	})
}

// assertNoInlineDependencies fails if any record's issue snapshot carries a
// "dependencies" array. The runtime snapshot (issue + labels only) never
// populates Dependencies, so a real record cannot contain one; a fixture that
// does would pin a shape bd never emits.
func assertNoInlineDependencies(t *testing.T, jsonl []byte) {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(jsonl), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Seq   int64           `json:"seq"`
			Issue json.RawMessage `json:"issue"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		if len(rec.Issue) == 0 || string(rec.Issue) == "null" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rec.Issue, &fields); err != nil {
			t.Fatalf("unmarshal issue for seq %d: %v", rec.Seq, err)
		}
		if _, ok := fields["dependencies"]; ok {
			t.Errorf("record seq %d carries an inline \"dependencies\" array, but the runtime snapshot (issue + labels only) never emits one; dependency edges belong only in the top-level \"dep\" field", rec.Seq)
		}
	}
}

// assertCommentSourcesAreEmittable fails if a comment record carries a source
// no emitter can produce.
//
// The golden shipped for one revision with source "comment", a value invented
// for the fixture: the engine only ever writes "structured" or "audit". A
// golden's whole job is to be the thing downstream consumers pin against, so a
// value bd cannot emit is worse than no fixture at all — it teaches a consumer
// to handle a case that will never arrive and, worse, to trust the file. The
// closed set comes from issueops rather than being restated here.
func assertCommentSourcesAreEmittable(t *testing.T, jsonl []byte) {
	t.Helper()
	emittable := map[string]bool{}
	for _, s := range issueops.CommentSources() {
		emittable[s] = true
	}
	if len(emittable) == 0 {
		t.Fatal("issueops declares no comment sources — this guard is not actually running")
	}
	seen := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(jsonl), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Seq     int64 `json:"seq"`
			Op      string
			Comment *struct {
				Source string `json:"source"`
			} `json:"comment"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		if rec.Comment == nil {
			continue
		}
		seen[rec.Comment.Source] = true
		if !emittable[rec.Comment.Source] {
			t.Errorf("record seq %d carries comment source %q, which no emit site produces; the engine writes only %v",
				rec.Seq, rec.Comment.Source, issueops.CommentSources())
		}
	}
	// Both sources must appear, or the golden pins only half the contract and a
	// consumer that only ever sees "structured" in the fixture may not handle
	// the other at all.
	for _, s := range issueops.CommentSources() {
		if !seen[s] {
			t.Errorf("no golden record carries comment source %q; every emittable source belongs in the fixture", s)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

func strptr(s string) *string { return &s }
