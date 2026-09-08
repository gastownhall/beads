package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
)

// The durable events journal, published as a read.
//
// This is the operation that lets a hosted consumer stop shelling out to
// `bd events tail`. The records it serves are the SAME records that command
// prints — literally, through eventsjournal.NewRecord, which is the one
// projection from a stored row onto the published envelope and is byte-pinned
// by the CLI's golden fixture. There is no wire struct in this package for a
// journal record and there must never be one: a mirror here would satisfy its
// own tests while serving a `ts` in a different shape or a `dep` that appeared
// when empty, and the consumer that notices is the one reconciling an HTTP
// mirror against a CLI export at three in the morning.
//
// WHAT IS NOT HERE, in the reads.go tradition. No seq arithmetic, no gap
// detection, no decision about whether a page is contiguous: that is
// issueops.ComputeEventsTruncation, which every read plumbing runs, and it
// reaches this file as a typed error that problem.go maps. A handler that
// decided for itself whether a batch was continuous would be a second retention
// contract, and the failure mode of getting it wrong is silent record loss —
// which is the one thing this feature exists to prevent.
//
// NO FOLLOW MODE ON THIS OPERATION, and adding one is not the way to get a
// push feed: `follow=true` would give one operationId two contracts — two media
// types, two lifetimes, two limit rules — and a client generated from the
// document could not describe either. The push feed is a SIBLING operation,
// events_watch.go, which consumes this same read path. `head` is what makes
// polling cheap to pace: a consumer that sees its last seq equal to `head`
// knows it is caught up and can back off, instead of inferring it from a short
// page.
//
// A poller is still the path that is always needed — it is what a dropped
// stream reconnects through, and it holds nothing between requests — which is
// why it landed first and why the stream's saturation refusal points back here.

// Journal paging bounds. There is no unlimited read here, unlike the issue
// listings: a caller resuming from an old checkpoint would otherwise ask one
// process to buffer the entire retained window — a hundred thousand records
// under the shipped retain-rows floor — and encode it into one response.
//
// The default is deliberately much larger than the issue listings' 50. A
// journal consumer is a machine draining a backlog in order, not a person
// reading a page, and its cost per record is a fraction of an issue row's; the
// interesting number for it is round trips to catch up, which at 50 would be
// two thousand for a full window.
const (
	defaultEventsLimit = 1000
	maxEventsLimit     = 10000
)

// handleListEvents answers GET /v0/beads/events.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	// Read BOTH parameters before deciding anything, so an unknown third one is
	// still reported by the shared rule rather than shadowed by the `since`
	// refusal below.
	since := q.integer64("since")
	limit := q.boundedLimit(defaultEventsLimit, maxEventsLimit)

	if !s.acceptQuery(w, r, q) {
		return
	}
	// `since` is required, and its absence is refused HERE rather than defaulted
	// to 0 — the tree walker's argument for `root_id`, with more at stake. A
	// missing checkpoint defaulted to zero would serve the whole retained window
	// to a consumer that meant to resume, which reads as a flood of duplicate
	// records rather than as an error.
	//
	// A NEGATIVE checkpoint is refused for the reason `bd events tail --since`
	// refuses one: it is almost always arithmetic on an empty cursor, and
	// `seq > -5` would quietly serve everything as though it were a legitimate
	// resume. q.integer64 has already refused a value that is not an integer at
	// all, so reaching here with a non-nil pointer means it parsed.
	if since == nil || *since < 0 {
		requestInfo(r.Context()).refuse("since")
		s.fail(w, r, InvalidArgument("since", ReasonInvalidValue,
			"since is required and must be zero or a positive sequence number; use 0 to read from the beginning"))
		return
	}

	// The activation gate, BEFORE the read. A disabled journal answers zero rows
	// and a head of zero, which is indistinguishable from an enabled journal
	// nothing has written to yet — so a consumer would poll a workspace that
	// will never produce a record and call itself caught up. This is the only
	// place the difference is knowable, and it costs no database round trip.
	if !s.cfg.EventsJournalEnabled {
		s.fail(w, r, EventsJournalDisabled())
		return
	}

	cursor, err := s.eventsJournalCursor(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	page, err := cursor.ReadEventsJournalPage(r.Context(), *since, limit)
	if err != nil {
		// A pruned-past checkpoint arrives as *storage.EventsJournalTruncatedError
		// and leaves as the 410 with its window attached; ClassifyError owns that
		// row, so this handler has no special case for it.
		s.failErr(w, r, err)
		return
	}

	writeEventsPage(w, page)
}

// writeEventsPage streams the answer instead of building it.
//
// writeJSON would cost two further whole-page copies on top of the rows the
// seam already handed back: one []Record slice, whose json.RawMessage members
// copy every stored payload out of its string, and then encoding/json's own
// buffer, which assembles the entire body before a byte reaches the socket. At
// the 10000-record ceiling with issue payloads of a few hundred bytes that is
// tens of megabytes of transient garbage per request, times the sixteen
// requests this server admits at once. Here each record is projected and
// encoded on its way out, so the live cost is one record rather than the page.
//
// FULL CURSOR STREAMING — reading rows off the database cursor and emitting
// them without materializing the page at all — is deliberately NOT what this
// does, and cannot be until the retention contract changes. The truncation
// check needs the whole batch in memory: firstEventsSeqGap makes a linear pass
// looking for an interior hole, and it must reach a verdict BEFORE the first
// byte of a 200 is written, because a gap discovered halfway through a body is
// no longer expressible as the 410 the contract promises. Materialized rows
// are bounded by the limit cap; the two copies removed here were not bounded
// by anything the caller could not raise.
//
// The member names, their order and the `head` type all come from
// apigen.EventsPage, which is the document's shape — not from this function's
// opinion of it. TestEventsStreamsExactlyTheGeneratedEnvelope encodes the same
// data through that generated struct and requires the two to be byte-identical,
// so this stays one wire shape rather than a second one that agrees today.
func writeEventsPage(w http.ResponseWriter, page storage.EventsJournalPage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// `head` LEADS, because that is the order the generated envelope serializes
	// its fields in and this body must be the same bytes. It happens to be the
	// better order for a streaming reader too — the pacing number arrives before
	// the page it describes — but the reason it is written this way is the pin.
	//
	// gosec's taint analysis flags every direct write to a ResponseWriter as a
	// possible XSS sink, because it cannot see a Content-Type. It is
	// application/json here, set above, and both writes below are the output of
	// encoding/json — a decimal integer and one marshaled record — never a
	// caller string reflected into a document. writeJSON is unflagged only
	// because the encoder hides the write, which is the same bytes.
	//nolint:gosec // G705: application/json body, JSON-encoded content, no HTML sink.
	if _, err := fmt.Fprintf(w, `{"head":%d,"records":[`, page.Head); err != nil {
		return
	}
	for i, row := range page.Rows {
		// json.Marshal rather than an Encoder: Encode appends a newline per
		// value, and while that is insignificant JSON whitespace it would put
		// this body one byte per record away from the generated envelope's, and
		// the byte-identity test below is what keeps the two one shape.
		encoded, err := json.Marshal(eventsjournal.NewRecord(row))
		if err != nil {
			// Unreachable for a well-formed row, and a truncated 200 is all the
			// client can be told either way: the status is already on the wire.
			// The alternative — buffering the whole body to keep the option of
			// a 500 — is the cost this function exists to avoid, and writeJSON
			// does not buy it either.
			return
		}
		if i > 0 {
			if _, err := w.Write([]byte(",")); err != nil {
				return
			}
		}
		//nolint:gosec // G705: as above — encoding/json output into an application/json body.
		if _, err := w.Write(encoded); err != nil {
			return
		}
	}
	// `records` is never null: an empty page closes an empty array here, so a
	// caught-up consumer decodes a list of length zero rather than having to
	// treat a missing member as one. The trailing newline is writeJSON's, kept
	// so this body and a generated one are the same BYTES rather than merely
	// the same document.
	_, _ = io.WriteString(w, "]}\n")
}
