package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// recordingIssues captures the filter the reader built, which is the only way
// to see what a handler actually asked storage for. Everything else about a
// read is observable from the response; the FILTER is not, and the filter is
// where the drift this design exists to prevent would live.
type recordingIssues struct {
	domain.IssueUseCase

	mu     sync.Mutex
	ready  []types.WorkFilter
	search []types.IssueFilter
	items  []*types.IssueWithCounts
}

func (f *recordingIssues) GetReadyWorkWithCounts(_ context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	f.mu.Lock()
	f.ready = append(f.ready, filter)
	f.mu.Unlock()
	return domain.SearchCountsPage{Items: f.items}, nil
}

func (f *recordingIssues) SearchIssuesWithCounts(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchCountsPage, error) {
	f.mu.Lock()
	f.search = append(f.search, filter)
	f.mu.Unlock()
	return domain.SearchCountsPage{Items: f.items}, nil
}

// The ready surface's defer-wake sweep reaches this before the read; nothing
// is deferred in the fixture, so it reports a no-op sweep.
func (f *recordingIssues) WakeExpiredDefers(context.Context) (issues, wisps int, err error) {
	return 0, 0, nil
}

func (f *recordingIssues) readyFilters() []types.WorkFilter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.WorkFilter(nil), f.ready...)
}

func (f *recordingIssues) searchFilters() []types.IssueFilter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.IssueFilter(nil), f.search...)
}

// emptyConfig is the workspace vocabulary a bare fixture has: no custom
// statuses, no custom types, no infra overrides. The list reader loads it
// through the unit of work before it builds a filter, so a read test that
// drives the list path has to answer it.
type emptyConfig struct{ domain.ConfigUseCase }

func (emptyConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) { return nil, nil }
func (emptyConfig) GetCustomTypes(context.Context) ([]string, error)                { return nil, nil }
func (emptyConfig) GetInfraTypes(context.Context) (map[string]bool, error)          { return nil, nil }

func newReadServer(t *testing.T, cfg Config) (*testServer, *recordingIssues) {
	t.Helper()
	rec := &recordingIssues{}
	cfg.Provider = &fakeProvider{issues: &fakeIssues{}, readIssues: rec, readConfig: emptyConfig{}}
	return newTestServer(t, cfg), rec
}

// TestReadyForwardsAnExplicitSortPolicy is the guard on the one default that
// changes the item SET rather than just its order.
//
// The storage layer maps an EMPTY sort policy to hybrid. A handler that
// forwarded an absent `sort` as "" would therefore serve hybrid while the
// frozen document still read `default: priority` — and hybrid demotes older
// high-priority work, so as soon as `limit` truncates, the page contains
// DIFFERENT ISSUES from the ones `bd ready` shows. The document tests pin the
// document; only this pins the handler.
//
// The wanted value is the LITERAL, not readySortDefault. Comparing the filter
// against the same constant the handler read would pass for every value that
// constant could take, including hybrid — the assertion would say only that
// the handler forwards its own default, which is not the property at risk.
// TestDefaultsMatchCLIFlags ties that literal to `bd ready --sort`'s
// registered default and to the frozen document, so all three move together or
// one of them fails.
func TestReadyForwardsAnExplicitSortPolicy(t *testing.T) {
	ts, rec := newReadServer(t, Config{})

	if resp := ts.get(t, "/v0/beads/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	filters := rec.readyFilters()
	if len(filters) != 1 {
		t.Fatalf("%d ready queries, want 1", len(filters))
	}
	if got := filters[0].SortPolicy; got != types.SortPolicy("priority") {
		t.Errorf("SortPolicy = %q, want \"priority\" — an empty policy is the storage layer's hybrid fallback, and hybrid re-SELECTS the page once the limit truncates",
			got)
	}
	// The shared limit default reaches storage too: the document states the
	// number, the CLI flag registers the same constant, and this is where the
	// server proves it uses that constant rather than a literal of its own.
	if got, want := filters[0].Limit, workapi.DefaultReadyLimit; got != want {
		t.Errorf("Limit = %d, want the shared default %d", got, want)
	}
}

// TestReadySortIsValidatedAgainstTheDocumentedEnum: an unrecognized policy is a
// 400 rather than a silent fallback, because a silent fallback would answer a
// question the client did not ask.
func TestReadySortIsValidatedAgainstTheDocumentedEnum(t *testing.T) {
	ts, rec := newReadServer(t, Config{})

	resp := ts.get(t, "/v0/beads/ready?sort=newest")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["param"] != "sort" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("body = %v, want invalid_argument on param sort with reason invalid_value", body)
	}
	if n := len(rec.readyFilters()); n != 0 {
		t.Errorf("%d ready queries ran; a refused request must not reach storage", n)
	}
}

// TestABuilderRefusalIsTheDocumentedBadRequest drives every row of
// invalidFilterParam end to end, through the real builders.
//
// The mapping is prose matching on this repository's own error strings, which
// is only safe while something fails when one of those strings is reworded.
// Nothing did: the builders' golden files record successful filters and never
// see a message, so `invalid issue type ` in particular was pinned nowhere and
// a reworded builder would have quietly demoted `?type=bogus` from 400
// invalid_value to 500 internal.
//
// metadata_field and has_metadata_key are here for a second reason: their keys
// used to be checked only in the SQL builder, whose error arrives wrapped in
// the storage method's name and therefore cannot be classified at all. The
// frozen document says an invalid key is a 400; it was a 500.
func TestABuilderRefusalIsTheDocumentedBadRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		param string
	}{
		{"an issue type outside the workspace vocabulary", "/v0/beads/issues?type=bogus", "type"},
		{"a status that is not a status", "/v0/beads/issues?status=bogus", "status"},
		{"a metadata field key the query layer cannot spell", "/v0/beads/issues?metadata_field=1bad=x", "metadata_field"},
		{"a has-metadata key the query layer cannot spell", "/v0/beads/issues?has_metadata_key=1bad", "has_metadata_key"},
		{"a metadata field key on the ready surface", "/v0/beads/ready?metadata_field=1bad=x", "metadata_field"},
		{"a has-metadata key on the ready surface", "/v0/beads/ready?has_metadata_key=1bad", "has_metadata_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newReadServer(t, Config{})
			resp := ts.get(t, tc.path)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("GET %s: status = %d, want 400", tc.path, resp.StatusCode)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("GET %s: body = %v, want invalid_argument on param %s with reason invalid_value", tc.path, body, tc.param)
			}
			// The detail reflects the caller's own input back, which is what a
			// 4xx detail is for — and is the half that stops being true if the
			// builder's message moves.
			if detail, _ := body["detail"].(string); detail == "" {
				t.Errorf("GET %s: no detail; the builder's own message is the detail", tc.path)
			}
		})
	}
}

// TestAnInvalidMetadataKeyNeverReachesStorage: the refusal is worth having only
// if it happens before the query runs. A key that reached the SQL builder came
// back as a 500 with the storage method's name wrapped around it.
func TestAnInvalidMetadataKeyNeverReachesStorage(t *testing.T) {
	ts, rec := newReadServer(t, Config{})
	if resp := ts.get(t, "/v0/beads/issues?metadata_field=1bad=x"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if n := len(rec.searchFilters()); n != 0 {
		t.Errorf("%d list queries ran; the refusal must happen before the database is touched", n)
	}
	if n := len(rec.readyFilters()); n != 0 {
		t.Errorf("%d ready queries ran; the refusal must happen before the database is touched", n)
	}
}

// TestUnknownReadParameterIsRefusedByName: silently ignoring an unrecognized
// FILTER parameter WIDENS the result set, so a client one version ahead
// receives rows it believes it filtered out.
func TestUnknownReadParameterIsRefusedByName(t *testing.T) {
	for _, path := range []string{"/v0/beads/ready?bogus=1", "/v0/beads/issues?bogus=1", "/v0/beads/issues/bd-1?bogus=1"} {
		ts, _ := newReadServer(t, Config{})
		resp := ts.get(t, path)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400", path, resp.StatusCode)
			continue
		}
		body := decodeBody(t, resp)
		if body["param"] != "bogus" || body["reason"] != string(ReasonUnknownParameter) {
			t.Errorf("GET %s: body = %v, want param=bogus reason=unknown_parameter", path, body)
		}
	}
}

// TestAMalformedKnownParameterIsNotReportedAsVersionSkew: the two 400 reasons
// carry opposite client recoveries — unknown_parameter says "this server is
// older than you think, degrade", invalid_value says "send something else" —
// so a bad value on a parameter the server DOES know must not be reported as
// the former just because the request also has to be checked for the latter.
func TestAMalformedKnownParameterIsNotReportedAsVersionSkew(t *testing.T) {
	ts, _ := newReadServer(t, Config{})

	resp := ts.get(t, "/v0/beads/ready?limit=-1&bogus=1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["param"] != "limit" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("body = %v, want the malformed known parameter reported first", body)
	}
}

// TestUnlimitedReadsAreLoopbackOnly pins the one mode-dependent refusal: an
// unlimited read buffers the whole active set and its JSON encoding inside one
// shared process, which must not be reachable by arbitrary network peers.
func TestUnlimitedReadsAreLoopbackOnly(t *testing.T) {
	t.Run("loopback allows it", func(t *testing.T) {
		ts, rec := newReadServer(t, Config{})
		if resp := ts.get(t, "/v0/beads/ready?limit=0"); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		filters := rec.readyFilters()
		if len(filters) != 1 || filters[0].Limit != 0 {
			t.Errorf("filters = %v, want one query with Limit 0 (unlimited passes through untouched)", filters)
		}
	})

	t.Run("a non-loopback bind refuses it", func(t *testing.T) {
		// InsecureNoAuth is what --allow-non-loopback now requires when no
		// token file is configured. It is the posture under test here — the
		// refusal being pinned is about the BIND, not about the credential.
		ts, rec := newReadServer(t, Config{Addr: "127.0.0.1:0", AllowNonLoopback: true, InsecureNoAuth: true})
		resp := ts.get(t, "/v0/beads/ready?limit=0")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		body := decodeBody(t, resp)
		if body["param"] != "limit" || body["reason"] != string(ReasonInvalidValue) {
			t.Errorf("body = %v, want invalid_argument on param limit", body)
		}
		if n := len(rec.readyFilters()); n != 0 {
			t.Errorf("%d queries ran; the refusal must happen before the database is touched", n)
		}
	})
}

// TestGetIssueRefusesAnImpossibleIDFromTheEdge: an id longer than the column,
// or one carrying a control character a percent-escape decoded to, names no row
// that can exist. Answering it from the edge costs nothing and tells the caller
// exactly what a read would have — and the SAME 404 a real miss gets, so a
// caller cannot map the server's notion of a well-formed id.
func TestGetIssueRefusesAnImpossibleIDFromTheEdge(t *testing.T) {
	long := ""
	for range types.MaxFieldLen + 1 {
		long += "x"
	}
	for _, id := range []string{long, "bd-%01"} {
		ts, rec := newReadServer(t, Config{})
		resp := ts.get(t, "/v0/beads/issues/"+id)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET issue %q: status = %d, want 404", id, resp.StatusCode)
			continue
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
			t.Errorf("GET issue %q: code = %v, want not_found", id, body["code"])
		}
		if n := len(rec.readyFilters()); n != 0 {
			t.Errorf("GET issue %q reached storage", id)
		}
	}
}

// TestCursorRoundTrips: the token is opaque and server-private, and the only
// thing that invalidates one is a change to the encoding.
//
// The fixture's CreatedAt has to be NON-ZERO for this test to test anything.
// decodeCursor rejects a zero instant — an empty position is one of its
// documented failure modes — so a fixture left at the zero time made the
// happy-path assertions unreachable and the test passed while asserting
// nothing, encodeCursor broken outright included.
func TestCursorRoundTrips(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	items := []*types.IssueWithCounts{{Issue: &types.Issue{ID: "bd-7", CreatedAt: created, Priority: 2}}}

	// Both served orders, because a round trip that held for one and lost a
	// member of the other's position would still page — into the wrong rows.
	for _, order := range []listOrder{orderCreated, orderPriority} {
		token := cursorFor(items, order)
		if token == "" {
			t.Fatalf("%s: cursorFor returned no token for a nonempty page", order)
		}
		pos, ok := decodeCursor(token, order)
		if !ok {
			t.Fatalf("%s: a token this server minted did not decode: %q", order, token)
		}
		if pos.ID != "bd-7" {
			t.Errorf("%s: decoded id = %q, want bd-7", order, pos.ID)
		}
		if !pos.CreatedAt.Equal(created) {
			t.Errorf("%s: decoded created_at = %s, want %s — the position is a keyset predicate, so a lossy instant skips or repeats rows", order, pos.CreatedAt, created)
		}
		if order == orderPriority {
			if pos.Priority == nil {
				t.Errorf("%s: decoded no priority; this order's position is (priority, created_at, id)", order)
			} else if *pos.Priority != 2 {
				t.Errorf("%s: decoded priority = %d, want 2", order, *pos.Priority)
			}
		} else if pos.Priority != nil {
			t.Errorf("%s: decoded a priority (%d) it has no place for", order, *pos.Priority)
		}
	}

	for _, bad := range []string{"", "v0.abc", "v1.!!!", "v1.", "v2.!!!", "v2.", "v3.abc", "not-a-cursor"} {
		if _, ok := decodeCursor(bad, orderCreated); ok {
			t.Errorf("decodeCursor(%q) succeeded; every unreadable token is the same client situation", bad)
		}
	}
}

// TestAReadRouteTimesTheUnitsOfWorkItsReaderOpens pins the one property of
// timedProvider.IssueReader that is invisible from reading it, and it is here
// because a reviewer proposed removing it: build the reader OVER THE WRAPPER,
// so every unit of work it opens goes through the wrapper's NewUOW and lands in
// this request's uow_ms.
//
// The tempting edit is `p.inner.IssueReader()` — "add the layer by recursion,
// the way every other decorator does". Recursion is right for a decorator whose
// layer is on the RESULT: telemetry's accessor recurses because it wraps the
// reader it gets back. This decorator's layer is on NewUOW, which only a reader
// holding THIS wrapper can reach, so recursion would hand back a reader bound
// to the untimed provider. It compiles, every other test still passes, and
// every read route reports uow_ms=0.000 from then on. So this asserts the
// NUMBER on the request line, which is the only place the difference shows.
func TestAReadRouteTimesTheUnitsOfWorkItsReaderOpens(t *testing.T) {
	// A provider that takes a measurable moment to hand out a unit of work, so
	// the field is checkable rather than a rounded zero either way.
	provider := &fakeProvider{
		issues:     &fakeIssues{},
		readIssues: &recordingIssues{},
		readConfig: emptyConfig{},
		delay:      5 * time.Millisecond,
	}
	ts := newTestServer(t, Config{Provider: provider})

	if resp := ts.get(t, "/v0/beads/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Two units of work, both through the wrapper: the ready surface's
	// defer-wake sweep, then the read span. A reader bound to the untimed
	// provider would report zero.
	if n := len(provider.openedUOWs()); n != 2 {
		t.Fatalf("opened %d units of work, want 2 (defer-wake sweep + read span)", n)
	}

	line := findLogLine(t, ts.stderr.String(), "op="+OpListReadyWork)
	if !strings.Contains(line, "uow_ms=") {
		t.Fatalf("read request line has no uow_ms field:\n%s", line)
	}
	if strings.Contains(line, "uow_ms=0.000") {
		t.Errorf("read request line reports no unit-of-work time though the provider took 5ms; the reader is bound to the untimed provider:\n%s", line)
	}
}

// TestListForwardsTheEphemeralPlaneParameter is the handler half of
// ListRequest.IncludeEphemeral. The parameter decides which TABLES the query
// reads, and nothing in the response distinguishes a merged page from a durable
// one when the fixture holds no wisps — so the recorded filter is the only place
// a dropped plumb-through is visible, which is exactly why the filter is
// recorded at all.
//
// The third case is the one that stops the parameter from being wired to the
// wrong knob: `include_infra` also admits the plane, so a handler that mapped
// `include_ephemeral` onto IncludeInfra would pass the first two and silently
// widen the answer by four issue types.
func TestListForwardsTheEphemeralPlaneParameter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		path          string
		wantSkipWisps bool
		wantInfraType bool
	}{
		{"absent leaves the durable listing alone", "/v0/beads/issues", true, false},
		{"include_ephemeral admits the plane and takes no type exclusion off", "/v0/beads/issues?include_ephemeral=true", false, false},
		{"include_infra is the wider one", "/v0/beads/issues?include_infra=true", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			if resp := ts.get(t, tc.path); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200", tc.path, resp.StatusCode)
			}
			filters := rec.searchFilters()
			if len(filters) != 1 {
				t.Fatalf("%d list queries, want 1", len(filters))
			}
			if got := filters[0].SkipWisps; got != tc.wantSkipWisps {
				t.Errorf("SkipWisps = %v, want %v", got, tc.wantSkipWisps)
			}
			infraExcluded := false
			for _, excluded := range filters[0].ExcludeTypes {
				if excluded == types.IssueType("message") {
					infraExcluded = true
				}
			}
			if infraExcluded == tc.wantInfraType {
				t.Errorf("ExcludeTypes = %v; the infra types are a TYPE exclusion and only include_infra takes it off", filters[0].ExcludeTypes)
			}
		})
	}
}

// TestTheEphemeralPlaneParameterIsTheSkewSignal pins the version-skew half.
//
// A client that sends `include_ephemeral` to a server built before this
// parameter existed gets 400 `unknown_parameter` — the designed signal that the
// server is older than the client thinks, whose recovery is to degrade rather
// than retry. That behavior is the shared decoder's, so what has to hold HERE
// is the other side of it: this build must CONSUME the parameter, because a
// handler that stopped reading it would answer the durable set and report the
// request as unknown-parameter-free, which is indistinguishable from success.
func TestTheEphemeralPlaneParameterIsTheSkewSignal(t *testing.T) {
	ts, _ := newReadServer(t, Config{})

	resp := ts.get(t, "/v0/beads/issues?include_ephemeral=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a parameter this build serves must not read as skew", resp.StatusCode)
	}

	// The neighbouring spelling a client might guess is NOT quietly accepted:
	// an unknown parameter is refused by name, which is what makes the 200
	// above mean something.
	resp = ts.get(t, "/v0/beads/issues?include_ephemerals=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["param"] != "include_ephemerals" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want param=include_ephemerals reason=unknown_parameter", body)
	}
}
