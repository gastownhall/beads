// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wave2c_journal_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/journalops"
)

// The unit half of client wave 2c's events journal: the three facts a correct
// server cannot show, because a correct server never produces the conditions
// they are about.
//
// The served tier runs the whole six-case contract against a real bd serve, so
// what is left here is the reconciliation this client had to invent. The
// UNCAPPED read is the sharp one: it is a role promise the operation refuses by
// value, so the loop that reconciles them only runs past 10000 records — a size
// no contract case seeds and no served test would reach without minutes of
// seeding.

// journalWire is a transport that answers prepared event pages in order and
// records the query each request carried.
type journalWire struct {
	recordingWire

	pages   []apigen.EventsPage
	problem error
	queries []url.Values
}

func (w *journalWire) Do(ctx context.Context, req wire.Request, out any) error {
	w.queries = append(w.queries, req.Query)
	if err := w.recordingWire.Do(ctx, req, out); err != nil {
		return err
	}
	if w.problem != nil {
		return w.problem
	}
	body, ok := out.(*apigen.EventsPage)
	if !ok {
		return nil
	}
	if len(w.pages) == 0 {
		*body = apigen.EventsPage{}
		return nil
	}
	*body = w.pages[0]
	if len(w.pages) > 1 {
		w.pages = w.pages[1:]
	}
	return nil
}

func journalOver(t *testing.T, w WireClient) journalops.Journal {
	t.Helper()
	store := New(testTarget(t), w, &apigen.ContextResponse{})
	cursor, ok := any(store).(storage.EventsJournalCursor)
	if !ok {
		t.Fatal("*Store does not implement storage.EventsJournalCursor")
	}
	return cursor
}

// journalPage builds a page of n synthetic records starting after `from`.
func journalPage(from int64, n int, head int64) apigen.EventsPage {
	records := make([]eventsjournal.Record, 0, n)
	for i := 1; i <= n; i++ {
		records = append(records, eventsjournal.Record{
			Seq: from + int64(i), TS: "2026-08-11T00:00:00Z", Op: "create",
			IssueID: "bd-" + strconv.FormatInt(from+int64(i), 10),
			Issue:   json.RawMessage(`{"id":"bd-1"}`),
		})
	}
	return apigen.EventsPage{Head: head, Records: records}
}

// TestUncappedJournalReadPagesTheHandlersBoundAndAnswersTheLastHead is
// L-events-paging, pinned where it can fail.
//
// The role says a limit of 0 is uncapped; the operation refuses `limit=0` by
// value and caps a page at 10000. A client that forwarded the 0 would get a 400
// on every uncapped read, and one that silently capped would answer a partial
// page beside a head saying there is more — a consumer stalling on a gap nobody
// told it about. This asserts the third answer: the loop, its advancing
// checkpoint, and the LAST page's head.
//
// The two full pages are the smallest shape that can fail: one page proves
// nothing about advancing, and a short second page is what stops the loop.
func TestUncappedJournalReadPagesTheHandlersBoundAndAnswersTheLastHead(t *testing.T) {
	// BOTH PAGES REPORT THE JOURNAL'S HEAD, which is what a real server does:
	// the head is the counter's, not the page's, so a bounded page reports a head
	// ABOVE its own last row and that is how a caller learns there is more. A
	// fixture whose first page reported its own last seq would be a server saying
	// "caught up" with seven records still to serve.
	const head = int64(eventsWireLimit + 7)
	w := &journalWire{pages: []apigen.EventsPage{
		journalPage(0, eventsWireLimit, head),
		journalPage(eventsWireLimit, 7, head),
	}}

	page, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("an uncapped read: %v", err)
	}
	if got := len(page.Rows); got != eventsWireLimit+7 {
		t.Errorf("the uncapped read answered %d rows, want %d: it stopped at the handler's page bound", got, eventsWireLimit+7)
	}
	if page.Head != head {
		t.Errorf("the uncapped read answered head %d, want the LAST page's %d: an earlier page's head is stale "+
			"the moment the next request is made", page.Head, head)
	}
	if len(w.queries) != 2 {
		t.Fatalf("the uncapped read made %d requests, want 2", len(w.queries))
	}
	if got := w.queries[0].Get("limit"); got != strconv.Itoa(eventsWireLimit) {
		t.Errorf("the first request sent limit=%q, want the handler's bound %d; 0 is a 400 on this operation",
			got, eventsWireLimit)
	}
	if got, want := w.queries[0].Get("since"), "0"; got != want {
		t.Errorf("the first request sent since=%q, want %q", got, want)
	}
	if got, want := w.queries[1].Get("since"), strconv.Itoa(eventsWireLimit); got != want {
		t.Errorf("the second request sent since=%q, want %q: the checkpoint advances to the last seq SERVED, "+
			"and since is exclusive, so anything else re-reads or skips", got, want)
	}

	// A BOUNDED read is one request and the bound travels as given.
	bounded := &journalWire{pages: []apigen.EventsPage{journalPage(0, 2, 9)}}
	if _, err := journalOver(t, bounded).ReadEventsJournalPage(context.Background(), 4, 2); err != nil {
		t.Fatalf("a bounded read: %v", err)
	}
	if len(bounded.queries) != 1 {
		t.Fatalf("a bounded read made %d requests, want 1", len(bounded.queries))
	}
	if got := bounded.queries[0].Get("limit"); got != "2" {
		t.Errorf("the bounded read sent limit=%q, want %q", got, "2")
	}
}

// TestJournalCheckpointSurvivesSixtyFourBits is the decode doctrine on the one
// member that carries a counter.
//
// `since` is an int64 on the role and on the wire, and the conformance suite's
// own head probe reads with math.MaxInt64 — a value an int conversion on a
// 32-bit build would wrap and a float64 decode would round. This asserts the
// value that reaches the query string is the value the caller gave.
func TestJournalCheckpointSurvivesSixtyFourBits(t *testing.T) {
	w := &journalWire{pages: []apigen.EventsPage{{Head: math.MaxInt64 - 1}}}
	page, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), math.MaxInt64, 1)
	if err != nil {
		t.Fatalf("a read at the top of the range: %v", err)
	}
	if got, want := w.queries[0].Get("since"), strconv.FormatInt(math.MaxInt64, 10); got != want {
		t.Errorf("the read sent since=%q, want %q", got, want)
	}
	if page.Head != math.MaxInt64-1 {
		t.Errorf("the head decoded as %d, want %d: a head past 2^53 must survive the decode", page.Head, math.MaxInt64-1)
	}
}

// TestJournalTruncationIsRebuiltAsTheTypedWindow is the one refusal on this seam
// that has to become a Go value carrying DATA rather than a sentinel.
//
// The contract checks it by errors.As against journalops.TruncatedError — a type
// no leg names, since storage.EventsJournalTruncatedError is an alias of it — and
// a client that returned the wire's sentinel alone would satisfy errors.Is and
// fail every consumer, because the recovery is a decision made from the WINDOW.
//
// THE SECOND ARM IS THE ONE THE CONTRACT CANNOT REACH: a server that answered the
// code without its three window members. A window rebuilt from absent members
// would carry zeros, and `Floor-1` computed from a zero is a resume that replays
// the whole journal — so the wire's own error travels unchanged instead.
func TestJournalTruncationIsRebuiltAsTheTypedWindow(t *testing.T) {
	since, floor, head := int64(40), int64(51), int64(99)
	w := &journalWire{problem: &wire.ProblemError{
		Op: wire.OpListEvents, Status: http.StatusGone, Code: "events_journal_truncated",
		Since: &since, Floor: &floor, Head: &head,
		Err: wire.ErrEventsJournalTruncated,
	}}

	_, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), 40, 0)
	var truncated *journalops.TruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("a pruned-past checkpoint answered %v, want *journalops.TruncatedError", err)
	}
	if truncated.Since != since || truncated.Floor != floor || truncated.Head != head {
		t.Errorf("the window is {%d %d %d}, want {%d %d %d}",
			truncated.Since, truncated.Floor, truncated.Head, since, floor, head)
	}

	bare := &journalWire{problem: &wire.ProblemError{
		Op: wire.OpListEvents, Status: http.StatusGone, Code: "events_journal_truncated",
		Err: wire.ErrEventsJournalTruncated,
	}}
	_, err = journalOver(t, bare).ReadEventsJournalPage(context.Background(), 40, 1)
	if errors.As(err, &truncated) {
		t.Errorf("a truncation with no window was rebuilt as %+v; a Floor of 0 makes `resume from Floor-1` "+
			"replay the whole journal", truncated)
	}
	if !errors.Is(err, wire.ErrEventsJournalTruncated) {
		t.Errorf("the unreconstructable truncation answered %v, want the wire's own error unchanged", err)
	}
}

// TestJournalRowKeepsThePayloadsRawAndTellsADeleteFromAMissingOne is the
// projection back onto the storage row, and the payload members are the part
// worth asserting.
//
// `issue` is ALWAYS present and carries the literal `null` on a delete, while
// `dep` and `comment` are ABSENT on the ops that have no such half. All three
// collapse to the empty string on the row, which is the row's own conflation —
// journalops.Row documents IssueJSON as empty when the op is a delete — and NOT
// one this client may introduce anywhere else: a payload that is present must
// travel byte for byte, because re-encoding would reorder members and
// renormalize numbers against a contract that promises the issue exactly as the
// mutation left it.
func TestJournalRowKeepsThePayloadsRawAndTellsADeleteFromAMissingOne(t *testing.T) {
	// A payload that does NOT survive a re-encode: the members are out of sorted
	// order and the number is past 2^53. Marshaling a decoded map would reorder
	// the first and round the second, which is exactly what the contract that
	// promises "the issue exactly as the mutation left it" forbids — and a
	// payload that happened to round-trip would let a re-encoding client pass.
	const raw = `{"title":"z","id":"bd-1","row_version":9007199254740993}`
	w := &journalWire{pages: []apigen.EventsPage{{Head: 3, Records: []eventsjournal.Record{
		{Seq: 1, TS: "t1", Op: "create", IssueID: "bd-1", Issue: json.RawMessage(raw)},
		{Seq: 2, TS: "t2", Op: "delete", IssueID: "bd-1", Issue: json.RawMessage("null")},
		{Seq: 3, TS: "t3", Op: "dep_add", IssueID: "bd-1",
			Issue: json.RawMessage(raw), Dep: json.RawMessage(`{"kind":"blocks"}`)},
	}}}}

	page, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(page.Rows) != 3 {
		t.Fatalf("read %d rows, want 3", len(page.Rows))
	}
	if page.Rows[0].IssueJSON != raw {
		t.Errorf("the create's payload is %q, want the bytes the server sent, %q", page.Rows[0].IssueJSON, raw)
	}
	if page.Rows[1].IssueJSON != "" {
		t.Errorf("the delete's payload is %q, want \"\": a delete has no surviving row, and the row type "+
			"spells that as an empty string", page.Rows[1].IssueJSON)
	}
	if page.Rows[0].DepJSON != "" || page.Rows[0].CommentJSON != "" {
		t.Errorf("the create carries dep %q and comment %q, want both empty: absence says the op has no such half",
			page.Rows[0].DepJSON, page.Rows[0].CommentJSON)
	}
	if page.Rows[2].DepJSON != `{"kind":"blocks"}` {
		t.Errorf("the edge's dep payload is %q, want it verbatim", page.Rows[2].DepJSON)
	}
	for i, row := range page.Rows {
		if row.Seq != int64(i+1) || row.TS == "" || row.Op == "" || row.IssueID == "" {
			t.Errorf("row %d lost an envelope member: %+v", i, row)
		}
	}
}

// TestUncappedJournalReadAnswersTheFreshestHeadAndFailsWholeOnALateTruncation
// is the pair of clauses L-events-paging claims SURVIVE the loop, and both need
// a server that CHANGES between pages — which is the whole point of the row and
// the one thing a single-instant fixture cannot show.
//
// THE HEAD. Every page reports the journal's own head, so a mutation committing
// mid-loop raises it. Answering the first page's head would tell a caller it was
// caught up at a number the journal had already passed, which is a poller that
// stops polling. The freshest head is the only one that cannot be behind the
// rows served.
//
// THE LATE TRUNCATION. A prune landing mid-loop makes a LATER page a 410. A loop
// that returned the prefix it had already gathered would answer a gap with a
// plausible-looking suffix and a nil error — silent data loss, which is the one
// failure a replay feed must never ship. The read fails whole.
func TestUncappedJournalReadAnswersTheFreshestHeadAndFailsWholeOnALateTruncation(t *testing.T) {
	t.Run("the head is the freshest one", func(t *testing.T) {
		// The first page reports a head five past its own last row, which is what
		// makes the loop continue; by the time the second page is assembled seven
		// more records have committed, so its head is higher again. The two heads
		// DIFFER, which is the only shape that can tell "the freshest" from "the
		// first".
		const seen = int64(eventsWireLimit + 5)
		const grown = int64(eventsWireLimit + 12)
		w := &journalWire{pages: []apigen.EventsPage{
			journalPage(0, eventsWireLimit, seen),
			{Head: grown, Records: journalPage(eventsWireLimit, 5, grown).Records},
		}}
		page, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), 0, 0)
		if err != nil {
			t.Fatalf("an uncapped read across a growing journal: %v", err)
		}
		if page.Head != grown {
			t.Errorf("the read answered head %d, want %d: an earlier page's head is behind the journal by "+
				"the time the next request lands, and a caller that acted on it would stop polling early",
				page.Head, grown)
		}
	})

	t.Run("a truncation on a later page fails the whole read", func(t *testing.T) {
		since, floor, head := int64(eventsWireLimit), int64(eventsWireLimit+40), int64(eventsWireLimit+90)
		w := &lateTruncationWire{
			first: journalPage(0, eventsWireLimit, head),
			problem: &wire.ProblemError{
				Op: wire.OpListEvents, Status: http.StatusGone, Code: "events_journal_truncated",
				Since: &since, Floor: &floor, Head: &head,
				Err: wire.ErrEventsJournalTruncated,
			},
		}
		page, err := journalOver(t, w).ReadEventsJournalPage(context.Background(), 0, 0)
		var truncated *journalops.TruncatedError
		if !errors.As(err, &truncated) {
			t.Fatalf("a prune landing mid-loop answered (%d rows, %v), want the typed truncation: a read that "+
				"kept the prefix it had gathered would answer a GAP with a plausible-looking suffix", len(page.Rows), err)
		}
		if len(page.Rows) != 0 {
			t.Errorf("the failed read carried %d rows; result values are unspecified when error is non-nil, and a "+
				"caller that ignored the error must get nothing rather than a partial answer", len(page.Rows))
		}
		if truncated.Floor != floor {
			t.Errorf("the window names floor %d, want the LATER page's %d", truncated.Floor, floor)
		}
	})
}

// lateTruncationWire answers one good page and then the prepared problem, which
// is the shape a prune landing mid-loop takes.
type lateTruncationWire struct {
	journalWire

	first    apigen.EventsPage
	problem  error
	answered bool
}

func (w *lateTruncationWire) Do(ctx context.Context, req wire.Request, out any) error {
	w.queries = append(w.queries, req.Query)
	if w.answered {
		return w.problem
	}
	w.answered = true
	if body, ok := out.(*apigen.EventsPage); ok {
		*body = w.first
	}
	return nil
}
