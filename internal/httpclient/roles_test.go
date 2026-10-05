// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/roles_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The database-free half of the read roles: the dispatch policy, the two
// constants this package had to copy, and the refusal shapes. None of it needs
// a server, so all of it runs in the default build rather than behind the cgo
// tag the served composition lives under.

// recordingWire records the operations a role dispatched and answers each one
// with an empty body.
//
// The embedded WriteWire is nil for the reason fakeWire's is: it makes this
// double satisfy the transport seam however the write beads grow it, and a read
// role that somehow reached a write operation panics rather than getting a
// plausible zero.
type recordingWire struct {
	WriteWire

	preflighted []string
	dispatched  []string
	requests    []wire.Request
}

func (r *recordingWire) ServerContext(context.Context) (*apigen.ContextResponse, error) {
	return &apigen.ContextResponse{}, nil
}

func (r *recordingWire) Preflight(_ context.Context, op string) error {
	r.preflighted = append(r.preflighted, op)
	return nil
}

func (r *recordingWire) Do(_ context.Context, req wire.Request, out any) error {
	r.dispatched = append(r.dispatched, req.Op)
	r.requests = append(r.requests, req)
	switch body := out.(type) {
	case *apigen.ReadyPage:
		*body = apigen.ReadyPage{Items: []apigen.IssueWithCounts{}}
	case *apigen.IssuesPage:
		*body = apigen.IssuesPage{Items: []apigen.IssueWithCounts{}}
	case *apigen.QueryPage:
		*body = apigen.QueryPage{Items: []apigen.IssueWithCounts{}}
	case *apigen.ReadyCount:
		*body = apigen.ReadyCount{}
	case *apigen.Setting:
		*body = apigen.Setting{}
	case *types.IssueDetails:
		*body = types.IssueDetails{}
	}
	return nil
}

func recordingStore(t *testing.T) (*Store, *recordingWire) {
	t.Helper()
	w := &recordingWire{}
	return New(testTarget(t), w, &apigen.ContextResponse{}), w
}

// TestEveryDispatchSiteHonorsTheTwoSpeedPolicy is D6's mechanism, checked at
// the seam rather than at each call site.
//
// The POLICY lives in the wire package — a baseline operation returns from
// Preflight immediately, every other one forces the handshake and consults the
// advertised token — so what this store owes is that no operation reaches the
// transport without going through it. Routing every role through one dispatch
// helper is how that is made true; this is what proves the helper is the only
// route, and it names the baseline classification of each operation these roles
// use so a reclassification upstream lands here rather than silently.
func TestEveryDispatchSiteHonorsTheTwoSpeedPolicy(t *testing.T) {
	ctx := context.Background()
	s, w := recordingStore(t)

	reader, err := s.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	if _, err := reader.Ready(ctx, issueops.ReadyRequest{}); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := reader.List(ctx, issueops.ListRequest{SortBy: "created"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := reader.Get(ctx, issueops.GetRequest{ID: "bd-1"}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	querier, err := s.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}
	if _, err := querier.Query(ctx, issueops.QueryRequest{Expression: "type=bug"}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	counter, err := s.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	if _, err := counter.CountReady(ctx, issueops.ReadyRequest{}); err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if _, err := s.GetConfig(ctx, "issue_prefix"); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if _, err := s.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{"bd-1"}}); err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}

	if len(w.preflighted) != len(w.dispatched) {
		t.Fatalf("%d pre-flights for %d dispatches (%v vs %v): a dispatch that skipped the pre-flight would "+
			"answer an older server's bare 404 as an entity's not_found",
			len(w.preflighted), len(w.dispatched), w.preflighted, w.dispatched)
	}
	for i, op := range w.dispatched {
		if w.preflighted[i] != op {
			t.Errorf("dispatch %d pre-flighted %q and dialed %q", i, w.preflighted[i], op)
		}
	}

	// The classification the pre-flight then applies. Stated here so a change to
	// the baseline set upstream is a failure in this package rather than an
	// extra round trip nobody notices on the hot work-distribution path.
	for op, baseline := range map[string]bool{
		wire.OpListReadyWork:  true,
		wire.OpListIssues:     true,
		wire.OpGetIssue:       true,
		wire.OpQueryIssues:    false,
		wire.OpCountReadyWork: false,
		wire.OpGetSetting:     false,
		// claimIssue is first-slice but NOT baseline: it forces the handshake so
		// the identity gate runs before a write lands (ga-b8ddd.11).
		wire.OpClaimIssue: false,
	} {
		if wire.IsBaseline(op) != baseline {
			t.Errorf("wire.IsBaseline(%q) = %v, want %v", op, wire.IsBaseline(op), baseline)
		}
	}
}

// TestDispatchWithoutATransportIsABuildWiringFault: a binary that registered
// this backend without linking a wire client is a build fault, not a user's,
// and the store says so rather than panicking on a nil.
func TestDispatchWithoutATransportIsABuildWiringFault(t *testing.T) {
	s := New(testTarget(t), nil, nil)
	reader, err := s.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	if _, err := reader.Ready(context.Background(), issueops.ReadyRequest{}); !errors.Is(err, ErrNoTransport) {
		t.Errorf("Ready with no transport = %v, want ErrNoTransport", err)
	}
}

// TestDefaultListLimitMatchesTheSharedDefault is the drift pin for the one
// constant this package had to copy. depguard denies internal/workapi here, but
// not to a test file, which is exactly the exemption this uses.
func TestDefaultListLimitMatchesTheSharedDefault(t *testing.T) {
	if defaultListLimit != workapi.DefaultListLimit {
		t.Errorf("defaultListLimit = %d, want workapi.DefaultListLimit = %d: a nil ListRequest.Limit would "+
			"then trim to a different page over http than it does locally", defaultListLimit, workapi.DefaultListLimit)
	}
}

// TestCompareIssuesByMatchesTheSharedComparator is the drift pin for the other
// copy: the display comparator the page epilogue applies.
//
// It is driven over a generated corpus rather than a handful of pairs because
// the interesting cases are the ones nobody writes down — equal keys, a nil
// ClosedAt against a set one, a title that differs only by case, ids whose
// natural and lexical orders disagree.
func TestCompareIssuesByMatchesTheSharedComparator(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	titles := []string{"alpha", "Alpha", "beta", ""}
	statuses := []types.Status{types.StatusOpen, types.StatusClosed, types.StatusInProgress}
	assignees := []string{"", "ana", "Bo"}

	corpus := make([]*types.Issue, 0, 40)
	for i := 0; i < 40; i++ {
		issue := &types.Issue{
			ID:        "bd-" + strconv.Itoa(rng.Intn(20)),
			Title:     titles[rng.Intn(len(titles))],
			Status:    statuses[rng.Intn(len(statuses))],
			Priority:  rng.Intn(4),
			IssueType: types.TypeTask,
			Assignee:  assignees[rng.Intn(len(assignees))],
			CreatedAt: base.Add(time.Duration(rng.Intn(5)) * time.Hour),
			UpdatedAt: base.Add(time.Duration(rng.Intn(5)) * time.Hour),
		}
		if rng.Intn(2) == 0 {
			closed := base.Add(time.Duration(rng.Intn(5)) * time.Hour)
			issue.ClosedAt = &closed
		}
		corpus = append(corpus, issue)
	}

	for _, sortBy := range []string{"", "priority", "created", "updated", "closed", "status", "id", "title", "type", "assignee", "nonesuch"} {
		for _, a := range corpus {
			for _, b := range corpus {
				if got, want := compareIssuesBy(a, b, sortBy), workapi.CompareIssuesBy(a, b, sortBy); got != want {
					t.Fatalf("compareIssuesBy(%q) = %d, workapi.CompareIssuesBy = %d for\n a: %+v\n b: %+v",
						sortBy, got, want, a, b)
				}
			}
		}
	}
}

// TestReadRolesRefuseWhatTheWireCannotCarry pins the refusal SHAPE the roles
// hand back, without a server: both sentinels are reachable off one value, and
// the ledger row travels with it so D7's third taxonomy text has a flag and a
// reason to print.
func TestReadRolesRefuseWhatTheWireCannotCarry(t *testing.T) {
	ctx := context.Background()
	s, w := recordingStore(t)
	reader, err := s.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	_, err = reader.List(ctx, issueops.ListRequest{IDFilter: "bd-1"})
	var inexpressible *InexpressibleError
	if !errors.As(err, &inexpressible) {
		t.Fatalf("List with an IDFilter = %v, want *InexpressibleError", err)
	}
	if inexpressible.Refused.Row.Field != "IDFilter" {
		t.Errorf("the refusal names %q, want IDFilter", inexpressible.Refused.Row.Field)
	}
	if inexpressible.Refused.Row.SpecRow == "" || inexpressible.Refused.Row.Why == "" {
		t.Error("the refusal carries a ledger row with no reason or no citation")
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Backend != Backend {
		t.Errorf("the refusal does not classify as *storage.ErrUnsupported for this backend: %v", err)
	}
	if !errors.Is(err, encode.ErrRefused) {
		t.Errorf("the refusal does not classify as encode.ErrRefused: %v", err)
	}
	if unsup.Op != "Reader.List" {
		t.Errorf("the refusal names Op %q, want the ROLE METHOD: nothing on the unsupported allowlist refuses this way", unsup.Op)
	}

	// A refusal never dials. The encoder decides it, so the round trip that
	// would have carried a filter the server cannot apply never happens.
	if len(w.dispatched) != 0 {
		t.Errorf("a refused request dialed %v", w.dispatched)
	}
}

// TestTheRolesOwnPageRefusalsAreValidationNotCapability separates the two kinds
// of "no" a caller can get, because the recovery differs: a request the ROLE
// forbids is the caller's to fix, and a request the WIRE cannot carry is a
// statement about this backend.
func TestTheRolesOwnPageRefusalsAreValidationNotCapability(t *testing.T) {
	ctx := context.Background()
	s, _ := recordingStore(t)

	counter, err := s.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	for _, req := range []issueops.ReadyRequest{
		{Limit: ptrTo(5)},
		{Limit: ptrTo(0)},
		{Offset: 1},
	} {
		if _, err := counter.CountReady(ctx, req); !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("CountReady(%+v) = %v, want ErrValidation: a cardinality has no page", req, err)
		}
	}

	querier, err := s.Querier()
	if err != nil {
		t.Fatalf("Querier(): %v", err)
	}
	if _, err := querier.Query(ctx, issueops.QueryRequest{Expression: "type=bug", Offset: -1}); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("Query with a negative Offset = %v, want ErrValidation", err)
	}
	if _, err := querier.Query(ctx, issueops.QueryRequest{Expression: "type=bug", Offset: 1, SortBy: "priority"}); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("Query with an Offset under a display order = %v, want ErrValidation", err)
	}
	// The one Offset shape that is this BACKEND's inability rather than the
	// request's fault classifies the other way.
	_, err = querier.Query(ctx, issueops.QueryRequest{Expression: "type=bug", Offset: 1})
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Errorf("Query with a plain Offset = %v, want *ErrUnsupported", err)
	}
}

// TestSearchIssuesRefusesAPartialIDWithItsOwnText pins D11's dedicated refusal.
// The raw "failed to search issues" fallthrough it replaces answers the wrong
// question: the input may be a partial id or a full id that does not exist.
func TestSearchIssuesRefusesAPartialIDWithItsOwnText(t *testing.T) {
	s, w := recordingStore(t)
	_, err := s.SearchIssues(context.Background(), "bd-a3f", types.IssueFilter{})
	var partial *PartialIDSearchError
	if !errors.As(err, &partial) {
		t.Fatalf("a substring SearchIssues = %v, want *PartialIDSearchError", err)
	}
	if partial.Input != "bd-a3f" {
		t.Errorf("the refusal echoes %q, want the caller's own input", partial.Input)
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Backend != Backend {
		t.Errorf("the refusal does not classify as *storage.ErrUnsupported: %v", err)
	}
	if len(w.dispatched) != 0 {
		t.Errorf("a partial-id refusal dialed %v; there is no operation to dial", w.dispatched)
	}
}

// TestExactIDFanOutIsOneGetIssuePerID pins the D11 fast path's wire shape: one
// getIssue per named id, in the caller's order, with no listing call at all.
func TestExactIDFanOutIsOneGetIssuePerID(t *testing.T) {
	s, w := recordingStore(t)
	if _, err := s.SearchIssues(context.Background(), "", types.IssueFilter{IDs: []string{"bd-1", "bd-2", "bd-3"}}); err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(w.requests) != 3 {
		t.Fatalf("the fan-out issued %d requests, want one per id", len(w.requests))
	}
	for i, want := range []string{"bd-1", "bd-2", "bd-3"} {
		if w.requests[i].Op != wire.OpGetIssue {
			t.Errorf("request %d dialed %q, want getIssue", i, w.requests[i].Op)
		}
		if w.requests[i].IssueID != want {
			t.Errorf("request %d names issue %q, want %q in the caller's order", i, w.requests[i].IssueID, want)
		}
	}
}

// TestTheExactIDsFanOutIsBounded: an unbounded id set would be an unbounded
// burst at one shared server, so the encoder bounds it and the store surfaces
// the refusal rather than the burst.
func TestTheExactIDsFanOutIsBounded(t *testing.T) {
	s, w := recordingStore(t)
	ids := make([]string, encode.MaxExactIDs+1)
	for i := range ids {
		ids[i] = "bd-" + strconv.Itoa(i)
	}
	if _, err := s.SearchIssues(context.Background(), "", types.IssueFilter{IDs: ids}); !errors.Is(err, encode.ErrRefused) {
		t.Errorf("a fan-out past the bound = %v, want the ledgered refusal", err)
	}
	if len(w.dispatched) != 0 {
		t.Errorf("a refused fan-out still dialed %d times", len(w.dispatched))
	}
}

func ptrTo[T any](v T) *T { return &v }
