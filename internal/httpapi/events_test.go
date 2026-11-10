package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
)

// The events journal read. Three of these cases are about a distinction the
// data cannot make on its own — caught-up versus disabled versus pruned-past —
// and those are the ones worth having: an implementation that gets the happy
// path right and collapses any two of those three is a consumer that stalls
// forever or loses records without ever seeing an error.

// eventsRows builds a contiguous run of journal rows starting at from.
func eventsRows(from int64, n int) []storage.EventsJournalRow {
	rows := make([]storage.EventsJournalRow, 0, n)
	for i := range n {
		seq := from + int64(i)
		rows = append(rows, storage.EventsJournalRow{
			Seq:       seq,
			TS:        "2026-01-02T03:04:05Z",
			Op:        "create",
			IssueID:   fmt.Sprintf("bd-%d", seq),
			IssueJSON: fmt.Sprintf(`{"id":"bd-%d"}`, seq),
		})
	}
	return rows
}

// journalServer stands up a roles-backed server over one journal fake, with the
// journal ENABLED — the ordinary configuration. The disabled case names itself.
func journalServer(t *testing.T, journal *roleEventsJournal) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{EventsJournal: journal, EventsJournalEnabled: true}))
}

// TestEventsServesTheRecordsAndTheHead is the happy path, and it asserts the
// RECORD SHAPE rather than only the count: these bodies are the same records
// `bd events tail` prints, and a wire struct that quietly diverged from the
// CLI's would pass a length check.
func TestEventsServesTheRecordsAndTheHead(t *testing.T) {
	journal := &roleEventsJournal{page: storage.EventsJournalPage{Rows: eventsRows(4, 3), Head: 9}}
	ts := journalServer(t, journal)

	resp := ts.get(t, "/v0/beads/events?since=3")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)

	if got := body["head"]; got != float64(9) {
		t.Errorf("head = %v, want 9", got)
	}
	records, _ := body["records"].([]any)
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	first, _ := records[0].(map[string]any)
	if got := first["seq"]; got != float64(4) {
		t.Errorf("records[0].seq = %v, want 4", got)
	}
	if got := first["issue_id"]; got != "bd-4" {
		t.Errorf("records[0].issue_id = %v, want bd-4", got)
	}
	if got := first["op"]; got != "create" {
		t.Errorf("records[0].op = %v, want create", got)
	}
	// Present with the ops that have no such half ABSENT, which is the envelope
	// contract rather than a formatting detail: absence says the op has no
	// dependency, where a null would say it had an empty one.
	if _, ok := first["dep"]; ok {
		t.Error("a create record carries `dep`; it must be absent")
	}
	if _, ok := first["issue"]; !ok {
		t.Error("`issue` is absent; it must always be present, carrying null on a delete")
	}

	// The checkpoint reached the seam unchanged. `since` is the whole cursor, so
	// a handler that dropped or shifted it would re-serve or skip records.
	if reads := journal.reads(); len(reads) != 1 || reads[0].since != 3 {
		t.Errorf("journal reads = %+v, want one read at since=3", reads)
	}
}

// TestEventsSerializesADeleteRecordAsANullIssue: `issue` is always present and
// carries the literal null on a delete, which is how a replaying consumer tells
// "this row is gone" from "this server failed to record the payload".
func TestEventsSerializesADeleteRecordAsANullIssue(t *testing.T) {
	journal := &roleEventsJournal{page: storage.EventsJournalPage{
		Rows: []storage.EventsJournalRow{{Seq: 1, TS: "2026-01-02T03:04:05Z", Op: "delete", IssueID: "bd-1"}},
		Head: 1,
	}}
	ts := journalServer(t, journal)

	resp := ts.get(t, "/v0/beads/events?since=0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	// Decoded generically, because the point is the JSON and not a Go zero
	// value: an absent member and an explicit null both decode to nil in a map.
	raw := readAll(t, resp)
	var body struct {
		Records []map[string]json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(body.Records))
	}
	issue, ok := body.Records[0]["issue"]
	if !ok {
		t.Fatal("`issue` is absent on a delete record; it must be present and null")
	}
	if string(issue) != "null" {
		t.Errorf("issue = %s, want null", issue)
	}
}

// TestEventsCaughtUpIsAnEmptyPage: a checkpoint at or past the head is a normal
// steady state for a poller, not a miss. A 404 here would make the surface's
// error vocabulary part of a consumer's happy path.
func TestEventsCaughtUpIsAnEmptyPage(t *testing.T) {
	journal := &roleEventsJournal{page: storage.EventsJournalPage{Head: 12}}
	ts := journalServer(t, journal)

	resp := ts.get(t, "/v0/beads/events?since=12")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	// The RAW body, because `records: null` and `records: []` both decode to an
	// empty slice and only one of them is the contract.
	raw := readAll(t, resp)
	var body struct {
		Records json.RawMessage `json:"records"`
		Head    int64           `json:"head"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if string(body.Records) != "[]" {
		t.Errorf("records = %s, want []", body.Records)
	}
	if body.Head != 12 {
		t.Errorf("head = %d, want 12", body.Head)
	}
}

// TestEventsEmptyJournalIsNotTheDisabledRefusal is the distinction the whole
// activation gate exists for. An enabled journal nothing has written to is a
// 200 with an empty page and a head of zero; a DISABLED one is a 409. A server
// that answered both the same way would tell a consumer polling a workspace
// that records nothing that it was caught up.
func TestEventsEmptyJournalIsNotTheDisabledRefusal(t *testing.T) {
	ts := journalServer(t, &roleEventsJournal{page: storage.EventsJournalPage{Head: 0}})

	resp := ts.get(t, "/v0/beads/events?since=0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := decodeBody(t, resp)["head"]; got != float64(0) {
		t.Errorf("head = %v, want 0", got)
	}
}

// TestEventsRefusesWhenTheJournalIsDisabled is the other half of that pair, and
// it also pins that the refusal costs no database work: a workspace that
// records nothing must not spend a slot and a transaction to say so.
func TestEventsRefusesWhenTheJournalIsDisabled(t *testing.T) {
	journal := &roleEventsJournal{page: storage.EventsJournalPage{Rows: eventsRows(1, 2), Head: 2}}
	ts := newTestServer(t, rolesConfig(Config{EventsJournal: journal, EventsJournalEnabled: false}))

	resp := ts.get(t, "/v0/beads/events?since=0")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["code"]; got != string(CodeEventsJournalDisabled) {
		t.Errorf("code = %v, want %q", got, CodeEventsJournalDisabled)
	}
	if got, _ := body["detail"].(string); got == "" {
		t.Error("no detail on the disabled refusal; it is the only place an operator learns what to set")
	}
	if n := len(journal.reads()); n != 0 {
		t.Errorf("the journal was read %d times behind a disabled gate, want 0", n)
	}
}

// TestEventsMapsTruncationToGone is the mapping this feature cannot ship
// without. A pruned-past checkpoint has to arrive as a typed 410 carrying the
// window the server CAN serve — the client's only two recoveries (resume from
// floor-1, or rebuild) are both computed from these three numbers.
func TestEventsMapsTruncationToGone(t *testing.T) {
	truncated := &storage.EventsJournalTruncatedError{Since: 2, Floor: 7, Head: 40}
	journal := &roleEventsJournal{err: truncated}
	ts := journalServer(t, journal)

	resp := ts.get(t, "/v0/beads/events?since=2")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["code"]; got != string(CodeEventsJournalTruncated) {
		t.Errorf("code = %v, want %q", got, CodeEventsJournalTruncated)
	}
	for _, tc := range []struct {
		member string
		want   float64
	}{{"since", 2}, {"floor", 7}, {"head", 40}} {
		if got := body[tc.member]; got != tc.want {
			t.Errorf("%s = %v, want %v", tc.member, got, tc.want)
		}
	}
	// The storage error's OWN sentence, so a consumer reading the CLI's failure
	// and this one sees a single description of a single condition.
	if got, _ := body["detail"].(string); got != truncated.Error() {
		t.Errorf("detail = %q, want the storage error's own message %q", got, truncated.Error())
	}
	// The code the CLI's --json failure carries, matched against storage's
	// constant rather than a literal: two spellings of one condition is exactly
	// what a consumer branching on `code` cannot survive.
	if string(CodeEventsJournalTruncated) != storage.EventsJournalTruncatedCode {
		t.Errorf("wire code %q has drifted from storage's %q", CodeEventsJournalTruncated, storage.EventsJournalTruncatedCode)
	}
}

// TestEventsMapsAWrappedTruncationToGone: the seam wraps its errors on the way
// out (a unit of work, a transaction helper), so the mapping has to be
// errors.As and not a type assertion. A wrapped truncation demoted to a 500 is
// the silent-loss case wearing a retryable status.
func TestEventsMapsAWrappedTruncationToGone(t *testing.T) {
	wrapped := fmt.Errorf("read events journal: %w", &storage.EventsJournalTruncatedError{Since: 0, Floor: 4, Head: 5})
	ts := journalServer(t, &roleEventsJournal{err: wrapped})

	resp := ts.get(t, "/v0/beads/events?since=0")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := decodeBody(t, resp)["floor"]; got != float64(4) {
		t.Errorf("floor = %v, want 4", got)
	}
}

// TestEventsAnOrdinaryFailureIsNotGone bounds the row above: only the typed
// truncation earns a 410. A journal read that failed for any other reason is
// the generic 500, because 410 tells a client its checkpoint is unusable and
// retrying is pointless — advice that would be wrong for a transient fault.
func TestEventsAnOrdinaryFailureIsNotGone(t *testing.T) {
	ts := journalServer(t, &roleEventsJournal{err: errors.New("connection reset by the database")})

	resp := ts.get(t, "/v0/beads/events?since=0")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["code"]; got != string(CodeInternal) {
		t.Errorf("code = %v, want %q", got, CodeInternal)
	}
	// The 5xx detail is fixed per code and the underlying text goes to the log
	// only; a journal error can carry a DSN like any other driver error.
	if got, _ := body["detail"].(string); got != staticDetail[CodeInternal] {
		t.Errorf("detail = %q, want the fixed 5xx string", got)
	}
}

// TestEventsRefusesABadCheckpoint. `since` is required and must be a
// non-negative integer; each refusal names the parameter, because `param` is
// what a client dispatches on.
func TestEventsRefusesABadCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "absent", query: ""},
		{name: "empty", query: "?since="},
		{name: "negative", query: "?since=-5"},
		{name: "not a number", query: "?since=abc"},
		{name: "fractional", query: "?since=1.5"},
		{name: "repeated", query: "?since=1&since=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := &roleEventsJournal{}
			ts := journalServer(t, journal)

			resp := ts.get(t, "/v0/beads/events"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["code"]; got != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
			}
			if got := body["param"]; got != "since" {
				t.Errorf("param = %v, want since", got)
			}
			if got := body["reason"]; got != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want %q", got, ReasonInvalidValue)
			}
			if n := len(journal.reads()); n != 0 {
				t.Errorf("the journal was read %d times for a refused request, want 0", n)
			}
		})
	}
}

// TestEventsRefusesAnUnknownParameterRatherThanTheCheckpoint: the shared
// unknown-parameter rule runs BEFORE the required-`since` refusal, so a client
// one version ahead learns that this server does not know its parameter instead
// of being told to fix a checkpoint it already sent.
func TestEventsRefusesAnUnknownParameterRatherThanTheCheckpoint(t *testing.T) {
	ts := journalServer(t, &roleEventsJournal{})

	resp := ts.get(t, "/v0/beads/events?since=0&follow=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["param"]; got != "follow" {
		t.Errorf("param = %v, want follow", got)
	}
	if got := body["reason"]; got != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %q", got, ReasonUnknownParameter)
	}
}

// TestEventsAppliesTheDocumentedLimitBounds. The default and the ceiling are
// asserted on the value that reached the SEAM, not on the response size: a
// handler that ignored `limit` entirely would return whatever the fake held and
// pass any assertion about the body.
func TestEventsAppliesTheDocumentedLimitBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		wantLimit int
		wantRefus bool
	}{
		{name: "absent takes the default", query: "?since=0", wantLimit: defaultEventsLimit},
		{name: "explicit is honored", query: "?since=0&limit=25", wantLimit: 25},
		{name: "the ceiling itself is legal", query: "?since=0&limit=10000", wantLimit: maxEventsLimit},
		{name: "one is legal", query: "?since=0&limit=1", wantLimit: 1},
		{name: "zero is refused, not unlimited", query: "?since=0&limit=0", wantRefus: true},
		{name: "negative is refused", query: "?since=0&limit=-1", wantRefus: true},
		{name: "above the ceiling is refused", query: "?since=0&limit=10001", wantRefus: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := &roleEventsJournal{}
			ts := journalServer(t, journal)

			resp := ts.get(t, "/v0/beads/events"+tc.query)
			if tc.wantRefus {
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
				}
				if got := decodeBody(t, resp)["param"]; got != "limit" {
					t.Errorf("param = %v, want limit", got)
				}
				if n := len(journal.reads()); n != 0 {
					t.Errorf("the journal was read %d times for a refused limit, want 0", n)
				}
				return
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reads := journal.reads()
			if len(reads) != 1 {
				t.Fatalf("journal reads = %d, want 1", len(reads))
			}
			if reads[0].limit != tc.wantLimit {
				t.Errorf("limit reaching the seam = %d, want %d", reads[0].limit, tc.wantLimit)
			}
		})
	}
}

// TestEventsLimitZeroIsNotUnlimitedHere is the one cross-operation trap on this
// surface: `limit=0` means UNLIMITED on the issue listings and is refused
// outright here. Reinterpreting it as the default would hand a caller a page a
// thousand records long when it asked for the whole journal, silently.
func TestEventsLimitZeroIsNotUnlimitedHere(t *testing.T) {
	journal := &roleEventsJournal{}
	ts := journalServer(t, journal)

	resp := ts.get(t, "/v0/beads/events?since=0&limit=0")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if got, _ := decodeBody(t, resp)["detail"].(string); got == "" {
		t.Error("no detail naming the accepted range")
	}
	if n := len(journal.reads()); n != 0 {
		t.Errorf("the journal was read %d times, want 0", n)
	}
}

// TestEventsAcceptsALargeCheckpoint: `since` is an int64 on the wire and in
// storage, so a checkpoint above 2^31 must survive the decode on every build.
// Parsing it as a platform `int` would make this work on a 64-bit server and
// fail on a 32-bit one.
func TestEventsAcceptsALargeCheckpoint(t *testing.T) {
	const big = int64(1) << 40
	journal := &roleEventsJournal{page: storage.EventsJournalPage{Head: big}}
	ts := journalServer(t, journal)

	resp := ts.get(t, fmt.Sprintf("/v0/beads/events?since=%d", big))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reads := journal.reads()
	if len(reads) != 1 || reads[0].since != big {
		t.Fatalf("journal reads = %+v, want one read at since=%d", reads, big)
	}
}

// TestListenRequiresTheJournalReaderOnlyWhenTheWorkspaceHasOne is the
// conditional half of the database-source check, and both polarities are the
// point.
//
// A workspace with the journal OFF must bind without a reader: the journal is
// off by default, and a storage backend with no journal seam at all is an
// ordinary backend as long as nobody asked it to record anything. Requiring one
// unconditionally would refuse to start servers that have no use for it.
//
// A workspace with the journal ON and no reader must be refused AT STARTUP,
// because the alternative is the shape checkDatabaseSource exists to prevent:
// a server that binds, answers every other route, and fails this one with a nil
// dereference on the first client that finds it.
func TestListenRequiresTheJournalReaderOnlyWhenTheWorkspaceHasOne(t *testing.T) {
	t.Run("off, no reader, binds", func(t *testing.T) {
		cfg := rolesConfig(Config{})
		cfg.EventsJournal = nil
		cfg.EventsJournalEnabled = false
		ts := newTestServer(t, cfg)

		resp := ts.get(t, "/v0/beads/events?since=0")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
		}
	})

	t.Run("on, no reader, refused at Listen", func(t *testing.T) {
		cfg := rolesConfig(Config{})
		cfg.EventsJournal = nil
		cfg.EventsJournalEnabled = true
		cfg.Addr = "127.0.0.1:0"

		srv, err := Listen(cfg)
		if err == nil {
			_ = srv.http.Close()
			t.Fatal("Listen bound a journal-enabled server with no EventsJournal reader")
		}
	})
}

// TestEventsStreamsExactlyTheGeneratedEnvelope is what lets the handler write
// the body itself without that becoming a second wire shape.
//
// writeEventsPage streams `records` a record at a time so the page is never
// buffered whole, which means the member names, their order and the trailing
// newline are spelled in that function rather than derived from
// apigen.EventsPage. This encodes the same data THROUGH the generated struct,
// exactly as writeJSON would have, and requires the two to be byte-identical —
// so a member renamed in the document, reordered by the generator, or given a
// different JSON type fails here instead of shipping.
func TestEventsStreamsExactlyTheGeneratedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		page storage.EventsJournalPage
	}{
		{name: "empty", page: storage.EventsJournalPage{Head: 0}},
		{name: "one record", page: storage.EventsJournalPage{Rows: eventsRows(1, 1), Head: 1}},
		{name: "several records", page: storage.EventsJournalPage{Rows: eventsRows(7, 4), Head: 40}},
		{
			// The three payload members at once, including the delete's null
			// issue and the absent dep/comment — the places the two encoders
			// could most plausibly disagree.
			name: "every payload shape",
			page: storage.EventsJournalPage{
				Rows: []storage.EventsJournalRow{
					{Seq: 1, TS: "2026-01-02T03:04:05Z", Op: "delete", IssueID: "bd-1"},
					{Seq: 2, TS: "2026-01-02T03:04:06Z", Op: "dep_add", IssueID: "bd-2",
						IssueJSON: `{"id":"bd-2","title":"a < b & c > d"}`,
						DepJSON:   `{"kind":"blocks","target":"bd-1","metadata":""}`},
					{Seq: 3, TS: "2026-01-02T03:04:07Z", Op: "comment", IssueID: "bd-2",
						IssueJSON:   `{"id":"bd-2"}`,
						CommentJSON: `{"id":"cmt-1","author":"worker","text":"note"}`},
				},
				Head: 3,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamed := httptest.NewRecorder()
			writeEventsPage(streamed, tc.page)

			generated := httptest.NewRecorder()
			writeJSON(generated, apigen.EventsPage{
				Records: eventsjournal.Records(tc.page.Rows),
				Head:    tc.page.Head,
			})

			if streamed.Body.String() != generated.Body.String() {
				t.Errorf("streamed body has drifted from the generated envelope\n streamed: %s\ngenerated: %s",
					streamed.Body.String(), generated.Body.String())
			}
			if got, want := streamed.Header().Get("Content-Type"), generated.Header().Get("Content-Type"); got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}
			if streamed.Code != generated.Code {
				t.Errorf("status = %d, want %d", streamed.Code, generated.Code)
			}
		})
	}
}

// TestEventsLimitBoundsMatchTheDocument. The default and the ceiling are stated
// twice — as Go constants the handler applies, and as `default`/`minimum`/
// `maximum` in the parameter the document publishes — and nothing else compares
// them. A client reads the document and sizes its pages from it; a server that
// had quietly moved either number would refuse requests the document promised
// to accept, which is version skew a client cannot detect.
func TestEventsLimitBoundsMatchTheDocument(t *testing.T) {
	doc := loadSpec(t)
	events := mapAt(t, mapAt(t, mapAt(t, doc, "paths"), "/v0/beads/events"), "get")

	params, _ := events["parameters"].([]any)
	var limit map[string]any
	for _, raw := range params {
		p, ok := raw.(map[string]any)
		if ok && p["name"] == "limit" {
			limit = mapAt(t, p, "schema")
			break
		}
	}
	if limit == nil {
		t.Fatal("the document publishes no `limit` parameter for listEvents")
	}

	for _, tc := range []struct {
		key  string
		want int
	}{
		{key: "default", want: defaultEventsLimit},
		{key: "minimum", want: 1},
		{key: "maximum", want: maxEventsLimit},
	} {
		got, ok := limit[tc.key].(int)
		if !ok {
			t.Errorf("limit schema has no integer %q (got %v)", tc.key, limit[tc.key])
			continue
		}
		if got != tc.want {
			t.Errorf("document says limit %s = %d, the handler applies %d", tc.key, got, tc.want)
		}
	}
}

// TestEventsIsReadOnly: the surface publishes GET and nothing else. A method
// this document does not describe gets the 404 every other unrouted request
// gets — 405 is not in the v0 vocabulary — and the point is that no prune,
// acknowledge or truncate verb exists here at all.
func TestEventsIsReadOnly(t *testing.T) {
	journal := &roleEventsJournal{}
	ts := journalServer(t, journal)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, ts.base+"/v0/beads/events?since=0", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := ts.client.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
		})
	}
	if n := len(journal.reads()); n != 0 {
		t.Errorf("the journal was reached %d times by a non-GET method, want 0", n)
	}
}
