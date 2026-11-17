// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/journal.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/journalops"
)

// The DURABLE MUTATION JOURNAL, read over GET /v0/beads/events.
//
// THERE IS NO ACCESSOR HERE, and its absence is the role's design rather than an
// omission: journalops.Journal is not on storage.DoltStorage, so a backend
// publishes the journal by IMPLEMENTING the interface a caller type-asserts for.
// `bd serve` makes exactly that assertion (cmd/bd's serveJournalCursor), which
// is why this method sits on *Store beside the raw off-role reads rather than
// behind a Journal() accessor nothing would call.
//
// THIS CLIENT HOLDS THE READ AND NOT THE OPERATOR HALF, which is the split
// journalops states and the reason the role exists apart from
// storage.EventsJournalAccessor. Retention and per-instance activation are
// decisions about a workspace's own recording, and this process has no workspace
// and no engine — it is a client of one. So *Store implements
// EventsJournalCursor and deliberately implements neither EventsJournalAccessor
// (which prunes) nor EventsJournalConfigurer (which activates), and
// cmd/bd/enterprise_events_journal_refusal_test.go pins all three facts about
// the type.
//
// WHAT THAT COSTS AT THE FRONT DOOR is worth stating rather than discovering:
// `bd events tail|export|prune` resolve their accessor through journalAccessor,
// which asks for the WIDE interface, so they still refuse on this backend. That
// is not a gap this port left open — it is the narrow interface doing its job. A
// command that only reads would need a read-only resolution path of its own.

// eventsWireLimit is the cap the HANDLER puts on one page, redeclared here
// because importing internal/httpapi would drag the storage engine into a
// client. It is maxEventsLimit in internal/httpapi/events.go.
//
// IT IS THE HANDLER'S PROMISE TO ITS CLIENTS rather than the role's: the role
// imposes no ceiling of its own, because the caller that pages a hundred
// thousand records out to a file is as legitimate as the one polling for ten.
// This client is on the other side of that promise, so an uncapped read becomes
// the loop in readPages rather than a refusal.
const eventsWireLimit = 10000

var _ journalops.Journal = (*Store)(nil)

// ReadEventsJournalPage returns the journal records after the caller's
// checkpoint, with the head of the journal's history.
//
// A LIMIT OF 0 IS UNCAPPED TO THE ROLE AND A 400 ON THE WIRE, and reconciling
// those two is the whole of what this body adds over one request. The role's
// contract is explicit that an uncapped read is legitimate; the operation
// refuses `limit=0` by value, because a page it could not bound is a page it
// could not serve. So an uncapped read becomes a LOOP over the handler's own
// maximum, advancing the checkpoint to the last seq served, until a short page
// or a checkpoint that has reached the head.
//
// THE ONE PROMISE THAT LOOP CANNOT KEEP is the one the Page type exists for: the
// rows and the head come from ONE transaction, and a loop is N of them. What
// survives is what a consumer acts on — the rows are still contiguous and
// seq-ascending, because each page continues exactly where the last ended, and
// the head is the LAST page's, which is the freshest and can only be at or ahead
// of the last row served. What does not survive is atomicity across the whole
// answer: a mutation committing mid-loop appears in a later page of the same
// call rather than in the next call. That is ledgered (L-events-paging) and it
// is strictly better than the alternatives — refusing an uncapped read the role
// permits, or silently capping one at 10000 and reporting a head that says there
// is more, which is a caller stalling on a page it was never told was partial.
//
// A BOUNDED READ IS ONE REQUEST and nothing above applies to it: limit reaches
// the wire as it was given, the page comes back with the journal's head, and a
// caller learns there is more by comparing the last seq against it.
func (s *Store) ReadEventsJournalPage(ctx context.Context, since int64, limit int) (journalops.Page, error) {
	if limit > 0 {
		return s.readEventsPage(ctx, since, limit)
	}
	return s.readEventsPages(ctx, since)
}

// readEventsPages walks the journal from since to its end, one handler-sized
// page at a time.
//
// The loop stops on a SHORT page, which is the end of the journal by the
// operation's own contract — records are contiguous, so a page smaller than the
// bound cannot be followed by another — and on a page that served nothing, which
// is the caught-up answer. It also stops at the head it was told, so a server
// that answered a full page of rows it had already served cannot spin this
// forever.
func (s *Store) readEventsPages(ctx context.Context, since int64) (journalops.Page, error) {
	page := journalops.Page{}
	cursor := since
	for {
		next, err := s.readEventsPage(ctx, cursor, eventsWireLimit)
		if err != nil {
			// A TRUNCATION ON ANY PAGE IS THE WHOLE READ'S, never a shorter
			// answer. The contract's bounded arm asserts the identical window
			// from a capped read and an uncapped one, and a loop that returned
			// the prefix it had already gathered would answer a gap with a
			// plausible-looking suffix — the one failure a replay feed must
			// never ship.
			return journalops.Page{}, err
		}
		page.Head = next.Head
		page.Rows = append(page.Rows, next.Rows...)
		if len(next.Rows) < eventsWireLimit {
			return page, nil
		}
		last := next.Rows[len(next.Rows)-1].Seq
		if last <= cursor || last >= next.Head {
			// The first arm is a server that did not advance; the second is a
			// caller that has reached the history it was told about. Neither is
			// a reason to ask again.
			return page, nil
		}
		cursor = last
	}
}

// readEventsPage is ONE request.
func (s *Store) readEventsPage(ctx context.Context, since int64, limit int) (journalops.Page, error) {
	q := url.Values{}
	// FormatInt rather than Itoa: `since` is an int64 and the conformance
	// suite's own head probe reads with math.MaxInt64, which a 32-bit int
	// conversion would silently wrap.
	q.Set("since", strconv.FormatInt(since, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}

	var body apigen.EventsPage
	if err := s.dispatch(ctx, wire.Request{
		Op:     wire.OpListEvents,
		Method: http.MethodGet,
		Path:   wire.PathEvents,
		Query:  q,
	}, &body); err != nil {
		return journalops.Page{}, eventsJournalError(err)
	}

	rows := make([]journalops.Row, 0, len(body.Records))
	for _, record := range body.Records {
		rows = append(rows, journalRow(record))
	}
	return journalops.Page{Rows: rows, Head: body.Head}, nil
}

// eventsJournalError rebuilds the role's TYPED truncation from the problem
// document that carried it.
//
// It is the one place on this seam where a refusal has to become a Go value
// carrying DATA rather than a sentinel, and the contract checks it by
// errors.As against journalops.TruncatedError — a type no leg names, since
// storage.EventsJournalTruncatedError is an alias of it. A client that returned
// the wire's sentinel alone would satisfy errors.Is and fail every consumer,
// because the recovery is a decision made from the WINDOW: resume from Floor-1
// and accept the gap, or rebuild.
//
// ALL THREE MEMBERS TRAVEL TOGETHER and none is omitted to mean zero — a head of
// 0 is a real journal state — so a document missing any of them is a server this
// client cannot reconstruct a window from, and the wire's own error travels
// unchanged rather than a window with a fabricated bound in it.
//
// `since` IS NOT ECHOED BACK. On an interior hole the server reports the last
// seq it could serve CONTIGUOUSLY from the caller's checkpoint, which is
// strictly more useful and cannot be recovered by a client that assumed it was
// getting its own input back — so this reads the member rather than the request.
func eventsJournalError(err error) error {
	if !errors.Is(err, wire.ErrEventsJournalTruncated) {
		return err
	}
	var prob *wire.ProblemError
	if !errors.As(err, &prob) || prob.Since == nil || prob.Floor == nil || prob.Head == nil {
		return err
	}
	return &journalops.TruncatedError{Since: *prob.Since, Floor: *prob.Floor, Head: *prob.Head}
}

// journalRow projects one published record back onto the storage row the role
// answers with.
//
// It is the exact inverse of eventsjournal.NewRecord, and the payload members
// are the part worth reading twice. `issue` is ALWAYS present and carries the
// literal `null` on a delete — a consumer must be able to tell a delete from a
// payload the server failed to record — while `dep` and `comment` are ABSENT on
// the ops that have no such half. All three collapse to the empty string on the
// row, which is the row's own conflation and not one this client introduces:
// journalops.Row documents IssueJSON as empty when the op is a delete.
//
// The payloads travel as RAW BYTES in both directions. Re-encoding them would
// reorder members and renormalize numbers against a contract that promises the
// issue exactly as the mutation left it.
func journalRow(record apigen.EventRecord) journalops.Row {
	row := journalops.Row{
		Seq:     record.Seq,
		TS:      record.TS,
		Op:      record.Op,
		IssueID: record.IssueID,
	}
	if payload := string(record.Issue); payload != "" && payload != "null" {
		row.IssueJSON = payload
	}
	row.DepJSON = string(record.Dep)
	row.CommentJSON = string(record.Comment)
	return row
}
