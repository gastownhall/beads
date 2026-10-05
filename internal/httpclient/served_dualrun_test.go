//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_dualrun_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Dual runs: the same store call, answered twice.
//
// Once by the reference store's own role, and once by the http client through
// the server that serves that very store. The two answers have to agree, and
// they are compared as the BYTES a caller receives rather than as a row count —
// `bd show --json` and `bd list --json` marshal these exact structs, so a field
// the wire round trip dropped or reshaped is a user-visible divergence even
// when the row set is right.
//
// The pair is the whole point. Every other test in this package asserts against
// values a human wrote down, which can only catch what that human thought to
// check; this one asserts against the local answer, which is the thing the
// backend is actually promising to reproduce.

func dualRunFixture(t *testing.T, name string) (*servedComposition, issueops.Reader, issueops.Reader, string) {
	t.Helper()
	ctx := t.Context()
	c, remote := servedReader(t)
	local, err := c.reference.IssueReader()
	if err != nil {
		t.Fatalf("reference IssueReader(): %v", err)
	}

	scope := servedIssuePrefix + "-dual-" + name
	base := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	for i, seed := range []struct {
		tag      string
		priority int
	}{{"a", 2}, {"b", 0}, {"c", 1}} {
		id := scope + "-" + seed.tag
		seedListIssue(t, ctx, c, id, scope, base.Add(time.Duration(i)*time.Minute), seed.priority)
	}
	// One edge and one comment, so the detail view and the cardinalities are
	// nonzero on at least one row: a dual run over rows with nothing hanging off
	// them cannot see a projection that drops a list.
	if err := c.seedDependency(ctx, &types.Dependency{
		IssueID: scope + "-a", DependsOnID: scope + "-b", Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the edge: %v", err)
	}
	if err := c.seedComment(ctx, scope+"-a", "seed", "a comment the detail view carries"); err != nil {
		t.Fatalf("seed the comment: %v", err)
	}
	return c, local, remote, scope
}

func assertSameJSON(t *testing.T, what string, local, remote any) {
	t.Helper()
	wantBytes, err := json.Marshal(local)
	if err != nil {
		t.Fatalf("marshal the reference answer for %s: %v", what, err)
	}
	gotBytes, err := json.Marshal(remote)
	if err != nil {
		t.Fatalf("marshal the http answer for %s: %v", what, err)
	}
	if string(wantBytes) != string(gotBytes) {
		t.Errorf("%s diverges\n  http: %s\n local: %s", what, gotBytes, wantBytes)
	}
}

// TestDualRunReaderAgreesWithTheReferenceStore is the smoke the composition
// exists for: Ready, List and Get answered by both, byte for byte.
func TestDualRunReaderAgreesWithTheReferenceStore(t *testing.T) {
	ctx := t.Context()
	_, local, remote, scope := dualRunFixture(t, "reader")

	ready := issueops.ReadyRequest{Labels: []string{scope}, Sort: "priority", Limit: ptrTo(0)}
	wantReady, err := local.Ready(ctx, ready)
	if err != nil {
		t.Fatalf("reference Ready: %v", err)
	}
	gotReady, err := remote.Ready(ctx, ready)
	if err != nil {
		t.Fatalf("http Ready: %v", err)
	}
	assertSameJSON(t, "Reader.Ready", wantReady, gotReady)

	for _, sortBy := range []string{"", "created", "priority", "id"} {
		list := issueops.ListRequest{Labels: []string{scope}, SortBy: sortBy, Limit: ptrTo(0)}
		wantList, err := local.List(ctx, list)
		if err != nil {
			t.Fatalf("reference List (sort %q): %v", sortBy, err)
		}
		gotList, err := remote.List(ctx, list)
		if err != nil {
			t.Fatalf("http List (sort %q): %v", sortBy, err)
		}
		assertSameJSON(t, "Reader.List (sort "+sortBy+")", wantList, gotList)
	}

	for _, get := range []issueops.GetRequest{
		{ID: scope + "-a"},
		{ID: scope + "-a", IncludeDependents: true, IncludeComments: true},
		{ID: scope + "-b", IncludeDependents: true},
	} {
		wantGet, err := local.Get(ctx, get)
		if err != nil {
			t.Fatalf("reference Get(%+v): %v", get, err)
		}
		gotGet, err := remote.Get(ctx, get)
		if err != nil {
			t.Fatalf("http Get(%+v): %v", get, err)
		}
		assertSameJSON(t, "Reader.Get", wantGet, gotGet)
	}
}

// TestDualRunCloseHistoryAgreesWithTheReferenceStore is the write-side dual run:
// the same multi-item batch close, answered once by the reference store's own
// BatchCloser and once by the http client through the server that serves it.
//
// It is the atomicity claim spelled as a comparison. Serving a batch close on ONE
// issues:batchClose call rather than a loop of closeIssue exists to keep the
// request's one-transaction/at-most-one-history-entry contract, and only a dual
// run against the local answer can show the wire round trip did not quietly turn
// N closes into N entries. Two mirror batches are seeded so each door closes its
// own, and both the durable history footprint and the per-item outcome shapes
// have to agree.
func TestDualRunCloseHistoryAgreesWithTheReferenceStore(t *testing.T) {
	ctx := t.Context()
	c, _, _, scope := dualRunFixture(t, "closehist")

	localCloser, err := c.reference.BatchCloser()
	if err != nil {
		t.Fatalf("reference BatchCloser(): %v", err)
	}
	remoteCloser, err := c.client.BatchCloser()
	if err != nil {
		t.Fatalf("http BatchCloser(): %v", err)
	}

	seed := func(prefix string, n int) []issueops.BatchCloseItem {
		items := make([]issueops.BatchCloseItem, n)
		for i := range items {
			id := scope + "-" + prefix + "-" + strconv.Itoa(i)
			issue := &types.Issue{
				ID: id, Title: id, Status: types.StatusOpen,
				IssueType: types.TypeTask, Labels: []string{scope},
			}
			if err := c.seedIssue(ctx, issue, "seed"); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
			items[i] = issueops.BatchCloseItem{IssueID: id, Reason: "done"}
		}
		return items
	}
	// Both mirror batches are seeded up front, so the only history the deltas
	// below can see is the close each door makes.
	localItems := seed("local", 3)
	remoteItems := seed("remote", 3)

	closeAndCount := func(name string, closer issueops.BatchCloser, items []issueops.BatchCloseItem) (issueops.CloseBatchResult, int) {
		before, err := c.countHistory(ctx)
		if err != nil {
			t.Fatalf("%s history before: %v", name, err)
		}
		res, err := closer.CloseBatch(ctx, issueops.CloseBatchRequest{Actor: "closer", Items: items, Session: "s"})
		if err != nil {
			t.Fatalf("%s CloseBatch: %v", name, err)
		}
		after, err := c.countHistory(ctx)
		if err != nil {
			t.Fatalf("%s history after: %v", name, err)
		}
		return res, after - before
	}

	localRes, localDelta := closeAndCount("reference", localCloser, localItems)
	remoteRes, remoteDelta := closeAndCount("http", remoteCloser, remoteItems)

	// The load-bearing parity: the wire round trip records the same durable
	// footprint the local batch does. A loop of closeIssue would show up here as
	// three entries against the reference's one.
	if remoteDelta != localDelta {
		t.Errorf("history delta diverges: the http batch close wrote %d entries, the reference wrote %d for the same batch shape",
			remoteDelta, localDelta)
	}
	// And the shape that parity is measured against: one transaction, at most one
	// entry, for a batch where every item landed.
	if localDelta != 1 {
		t.Errorf("a %d-item reference batch recorded %d history entries, want exactly one (batchcloser.go's atomicity clause)",
			len(localItems), localDelta)
	}

	assertBatchOutcomesAgree(t, localRes, remoteRes)
}

// assertBatchOutcomesAgree compares two batch results that closed mirror issue
// sets. The ids differ by construction, so the comparison is structural: the
// same count, the same landed/refused verdict per index, and the same Changed
// and OpenChildren — the fields a wire round trip could drop or reshape.
func assertBatchOutcomesAgree(t *testing.T, local, remote issueops.CloseBatchResult) {
	t.Helper()
	if len(local.Outcomes) != len(remote.Outcomes) {
		t.Fatalf("outcome counts diverge: reference %d, http %d", len(local.Outcomes), len(remote.Outcomes))
	}
	for i := range local.Outcomes {
		l, r := local.Outcomes[i], remote.Outcomes[i]
		if (l.Err == nil) != (r.Err == nil) {
			t.Errorf("outcome %d: reference err=%v, http err=%v — the landed/refused verdict diverges", i, l.Err, r.Err)
			continue
		}
		if (l.Issue == nil) != (r.Issue == nil) {
			t.Errorf("outcome %d: reference issue-present=%t, http issue-present=%t", i, l.Issue != nil, r.Issue != nil)
		}
		if l.Changed != r.Changed {
			t.Errorf("outcome %d: reference Changed=%t, http Changed=%t", i, l.Changed, r.Changed)
		}
		if l.OpenChildren != r.OpenChildren {
			t.Errorf("outcome %d: reference OpenChildren=%d, http OpenChildren=%d", i, l.OpenChildren, r.OpenChildren)
		}
	}
	if (local.ClaimedNext == nil) != (remote.ClaimedNext == nil) {
		t.Errorf("ClaimedNext presence diverges: reference=%t, http=%t", local.ClaimedNext != nil, remote.ClaimedNext != nil)
	}
}

// TestDualRunQuerierAndReadyCounterAgreeWithTheReferenceStore is the same smoke
// for the other two roles this bead flipped.
func TestDualRunQuerierAndReadyCounterAgreeWithTheReferenceStore(t *testing.T) {
	ctx := t.Context()
	c, _, _, scope := dualRunFixture(t, "roles")

	localQuerier, err := c.reference.Querier()
	if err != nil {
		t.Fatalf("reference Querier(): %v", err)
	}
	remoteQuerier, err := c.client.Querier()
	if err != nil {
		t.Fatalf("http Querier(): %v", err)
	}
	for _, req := range []issueops.QueryRequest{
		{Expression: "type=task AND label=" + scope, Limit: ptrTo(0)},
		{Expression: "(type=task OR type=bug) AND label=" + scope, SortBy: "priority", Limit: ptrTo(0)},
		{Expression: "(type=task OR type=bug) AND label=" + scope, SortBy: "priority", Reverse: true, Limit: ptrTo(2)},
	} {
		want, err := localQuerier.Query(ctx, req)
		if err != nil {
			t.Fatalf("reference Query(%q): %v", req.Expression, err)
		}
		got, err := remoteQuerier.Query(ctx, req)
		if err != nil {
			t.Fatalf("http Query(%q): %v", req.Expression, err)
		}
		assertSameJSON(t, "Querier.Query("+req.Expression+")", want, got)
	}

	localCounter, err := c.reference.ReadyCounter()
	if err != nil {
		t.Fatalf("reference ReadyCounter(): %v", err)
	}
	remoteCounter, err := c.client.ReadyCounter()
	if err != nil {
		t.Fatalf("http ReadyCounter(): %v", err)
	}
	req := issueops.ReadyRequest{Labels: []string{scope}, Sort: "priority"}
	want, err := localCounter.CountReady(ctx, req)
	if err != nil {
		t.Fatalf("reference CountReady: %v", err)
	}
	got, err := remoteCounter.CountReady(ctx, req)
	if err != nil {
		t.Fatalf("http CountReady: %v", err)
	}
	assertSameJSON(t, "ReadyCounter.CountReady", want, got)
}

// TestDualRunIDResolutionProbesAgreeWithTheReferenceStore covers D11's three
// store probes as ResolvePartialID makes them: the exact-ids fast path, the two
// prefix config reads, and the substring pass that has no wire operation.
func TestDualRunIDResolutionProbesAgreeWithTheReferenceStore(t *testing.T) {
	ctx := t.Context()
	c, _, _, scope := dualRunFixture(t, "resolve")
	present, absent := scope+"-a", scope+"-never-seeded"

	// The shapeless filter — neither an id set nor a parent — is the unbounded
	// "everything" question, and it refuses rather than answering it.
	if _, err := c.client.SearchIssues(ctx, "", types.IssueFilter{}); err == nil {
		t.Error("a shapeless SearchIssues answered instead of refusing")
	} else if !errors.Is(err, encode.ErrRefused) {
		t.Errorf("a shapeless SearchIssues refused with %v, want the encoder's ledgered refusal", err)
	}

	for _, ids := range [][]string{{present}, {absent}, {present, absent}} {
		filter := types.IssueFilter{IDs: ids}
		want, err := c.reference.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("reference SearchIssues(%v): %v", ids, err)
		}
		got, err := c.client.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("http SearchIssues(%v): %v", ids, err)
		}
		if len(got) != len(want) {
			t.Fatalf("SearchIssues(%v) returned %d rows, reference returned %d", ids, len(got), len(want))
		}
		for i := range want {
			assertSameJSON(t, "SearchIssues row", want[i], got[i])
		}
	}

	// The exact-hit fast path is the one ResolvePartialID reads as `err == nil &&
	// len(issues) > 0`, and the exact MISS has to be an empty result with a NIL
	// error or resolution stops where it should fall through.
	miss, err := c.client.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{absent}})
	if err != nil || len(miss) != 0 {
		t.Errorf("SearchIssues on an exact miss = (%d rows, %v), want (0, nil): a 404 is the empty result the resolver expects", len(miss), err)
	}

	// The prefix probes.
	if err := c.reference.SetConfig(ctx, "allowed_prefixes", "bd,http"); err != nil {
		t.Fatalf("seed allowed_prefixes: %v", err)
	}
	// "no-such-setting" rather than "no-such-key": the server redacts by KEY
	// SPELLING, and anything containing "key" is one of the spellings it treats
	// as credential-bearing — which is ledger row L9 and is asserted on its own
	// below, not by accident here.
	for _, key := range []string{"issue_prefix", "allowed_prefixes", "no-such-setting"} {
		want, err := c.reference.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("reference GetConfig(%q): %v", key, err)
		}
		got, err := c.client.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("http GetConfig(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("GetConfig(%q) = %q, reference answered %q", key, got, want)
		}
	}

	// Ledger row L9: a credential-bearing key is absent WITH A REASON rather than
	// absent as an empty string. The server decides redaction from the key alone,
	// so this holds whether or not the workspace stores anything under it — and
	// "" would be a dropped read dressed as an unset key.
	if _, err := c.client.GetConfig(ctx, "some.api_key"); !errors.Is(err, ErrSettingRedacted) {
		t.Errorf("GetConfig on a credential-bearing key = %v, want the withheld-with-reason refusal", err)
	}

	// The substring pass. It has no wire operation, and the refusal is D11's own
	// text rather than a bare sentinel, because the input may be a partial id OR
	// a full id that does not exist and the client cannot tell which.
	_, err = c.client.SearchIssues(ctx, "partial", types.IssueFilter{})
	var partial *PartialIDSearchError
	if !errors.As(err, &partial) {
		t.Fatalf("a substring SearchIssues = %v, want *PartialIDSearchError", err)
	}
	if partial.Input != "partial" || partial.ServerURL != c.baseURL {
		t.Errorf("the refusal names input %q on server %q, want %q on %q", partial.Input, partial.ServerURL, "partial", c.baseURL)
	}
	if !errors.Is(err, ErrPartialIDSearch) {
		t.Errorf("the refusal does not classify as ErrPartialIDSearch: %v", err)
	}
}

// TestTheCompositionCatchesAWrongParameter is the RED evidence.
//
// Each arm rewrites one correctly encoded request into a subtly wrong one and
// asserts the composition NOTICES. Without them every green test in this file
// would be equally green against a client that dropped a filter, because a
// dropped filter answers with MORE rows and every assertion here is written
// against rows that exist.
func TestTheCompositionCatchesAWrongParameter(t *testing.T) {
	ctx := t.Context()
	c, _, remote, scope := dualRunFixture(t, "tamper")

	honest, err := remote.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id", Limit: ptrTo(0)})
	if err != nil {
		t.Fatalf("the honest List: %v", err)
	}
	if len(honest.Items) != 3 {
		t.Fatalf("the honest List returned %v, want the three rows this case seeded", pageIDs(honest))
	}

	t.Run("a dropped filter widens the answer", func(t *testing.T) {
		dropped := c.tamperedClient(t, func(req *wire.Request) { req.Query.Del("label") })
		reader, err := dropped.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader(): %v", err)
		}
		page, err := reader.List(ctx, issueops.ListRequest{Labels: []string{scope}, SortBy: "id", Limit: ptrTo(0)})
		if err != nil {
			t.Fatalf("List with the label parameter dropped: %v", err)
		}
		if slices.Equal(pageIDs(page), pageIDs(honest)) {
			t.Fatal("dropping the `label` parameter changed nothing: this composition cannot see a dropped filter, " +
				"which is the one failure class no server-side gate can observe")
		}
	})

	t.Run("a misspelled parameter is refused rather than ignored", func(t *testing.T) {
		misspelled := c.tamperedClient(t, func(req *wire.Request) {
			if req.Query.Has("include_comments") {
				req.Query.Del("include_comments")
				req.Query.Set("includeComments", "true")
			}
		})
		reader, err := misspelled.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader(): %v", err)
		}
		_, err = reader.Get(ctx, issueops.GetRequest{ID: scope + "-a", IncludeComments: true})
		if !errors.Is(err, issueops.ErrValidation) {
			t.Fatalf("a misspelled parameter = %v, want the server's unknown_parameter refusal", err)
		}
		var problem *wire.ProblemError
		if !errors.As(err, &problem) || problem.Reason != "unknown_parameter" {
			t.Errorf("the refusal is %v, want reason \"unknown_parameter\" — the signal D7's case-3 text is built from", err)
		}
	})

	t.Run("a wrong sort policy answers a different order", func(t *testing.T) {
		// Its own rows, chosen so the two policies DISAGREE: the newer row is
		// the higher priority, so `priority` leads with it and `oldest` leads
		// with the other. Against rows where the orders coincide, a client that
		// sent the wrong policy would be invisible — which is the whole reason
		// the encoder sends a concrete policy rather than omitting an empty one.
		sortScope := scope + "-sort"
		now := time.Now().UTC()
		urgent := &types.Issue{
			ID: sortScope + "-urgent", Title: "urgent", Status: types.StatusOpen, Priority: 0,
			IssueType: types.TypeTask, Labels: []string{sortScope}, CreatedAt: now.Add(-time.Hour),
		}
		ancient := &types.Issue{
			ID: sortScope + "-ancient", Title: "ancient", Status: types.StatusOpen, Priority: 3,
			IssueType: types.TypeTask, Labels: []string{sortScope}, CreatedAt: now.Add(-90 * 24 * time.Hour),
		}
		for _, issue := range []*types.Issue{urgent, ancient} {
			if err := c.seedIssue(ctx, issue, "seed"); err != nil {
				t.Fatalf("seed %s: %v", issue.ID, err)
			}
		}

		req := issueops.ReadyRequest{Labels: []string{sortScope}, Sort: "priority", Limit: ptrTo(0)}
		honestReady, err := remote.Ready(ctx, req)
		if err != nil {
			t.Fatalf("the honest Ready: %v", err)
		}
		if want := []string{urgent.ID, ancient.ID}; !slices.Equal(pageIDs(honestReady), want) {
			t.Fatalf("the honest Ready --sort priority = %v, want %v", pageIDs(honestReady), want)
		}

		flipped := c.tamperedClient(t, func(req *wire.Request) {
			if req.Query.Get("sort") == "priority" {
				req.Query.Set("sort", "oldest")
			}
		})
		reader, err := flipped.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader(): %v", err)
		}
		tamperedReady, err := reader.Ready(ctx, req)
		if err != nil {
			t.Fatalf("Ready with the sort policy rewritten: %v", err)
		}
		if slices.Equal(pageIDs(honestReady), pageIDs(tamperedReady)) {
			t.Fatalf("rewriting `sort` changed nothing (%v): the ready policy this client sends is unobservable here",
				pageIDs(honestReady))
		}
	})
}
