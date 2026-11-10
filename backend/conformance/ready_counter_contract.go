package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.ReadyCounter must satisfy. Each case asserts what
// issueops/readycounter.go PROMISES, cited by line, rather than what any one
// backend happens to do; a backend that disagrees is parked at its own wiring
// site with skipKnownDivergence so the case still runs on the ones that agree.
//
// THERE ARE TWO BODIES BEHIND THE THREE WIRINGS, and here they are further
// apart than usual. dolt and embeddeddolt share internal/workapi/
// storereadycounter, which is one indexed COUNT(*) per plane minus their
// overlap; the unit-of-work provider runs the unbounded ready query and takes
// its length. So the wirings are one vote plus an engine check on the cheap
// path and a second, independent vote on the expensive one, and the cases below
// aim at the arithmetic only the first of them does.
//
// EVERY CASE IS SCOPED BY A LABEL: the ready front is a property of the whole
// database, not of an id prefix, so a row seeded by one assertion is inside the
// next one's answer unless each asks through Filter.Labels.
//
// EVERY CASE NAMES A SORT POLICY even though a count has no order.
// ReadyRequest.Sort's empty value resolves to hybrid at the storage layer, which
// the leaf says no front door may rely on (issueops/reader.go:79-83), and a
// count whose request is not the listing's request is not the count this role
// promises.

// ReadyCounterFixture supplies adapter-specific storage access for the
// ready-count assertions.
type ReadyCounterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// ReadyCounter is the surface under test.
	ReadyCounter publicops.ReadyCounter
	// Reader is the SAME backend's reader accessor. The role's central promise
	// is an IDENTITY with len(Reader.Ready(r with Limit=0).Items)
	// (issueops/readycounter.go:60-70), so both surfaces have to be asked.
	Reader publicops.Reader
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, which is how a case takes a row OFF the
	// ready front without deleting or closing it.
	AddDependency func(context.Context, *types.Dependency, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. A count case reads rows through it for one
	// reason: to prove a row this role must NOT count was really seeded in the
	// state that excludes it. Both surfaces this contract compares hide such a
	// row, so neither can tell "excluded" from "never written".
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case
	// that needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunReadyCounterEqualsTheUnboundedPage pins the identity that IS this role
// (issueops/readycounter.go:60-70): CountReady(r).Total equals
// len(Reader.Ready(r with Limit=0).Items), for every request the method
// accepts.
//
// The failure this exists to prevent is a total that describes a different set
// than the page it is printed beside: `bd ready` publishes both numbers as
// "showing 2 of 5", and a reader takes the second as "how much work is left".
//
// The bounded page is the half that matters. A count computed by re-running
// the listing would pass an unbounded comparison trivially; a count computed
// from a COUNT(*) over a predicate assembled somewhere else is where the two
// come apart, and it only shows up once a limit makes the numbers differ.
//
// The high-priority OUTSIDER is ready and outside the scope both surfaces are
// asked about, so it is counted by any request whose label predicate went
// missing between them.
func RunReadyCounterEqualsTheUnboundedPage(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rceq"
	for i, id := range readyCounterIDs(fixture, "rceq", 5) {
		seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(id, i%4, label))
	}
	outsider := fixture.IssuePrefix + "-rceq-outsider"
	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(outsider, 0, label+"-other"))

	request := publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort}
	unbounded := readyCounterPageIDs(t, ctx, fixture, request, 0)
	if len(unbounded) != 5 {
		t.Fatalf("Reader.Ready listed %d rows (%v) for the 5 this case seeded; "+
			"the identity below would then be an identity with the wrong set", len(unbounded), unbounded)
	}
	if page := readyCounterPageIDs(t, ctx, fixture, request, 2); len(page) != 2 {
		t.Fatalf("Reader.Ready with Limit=2 returned %d rows (%v), want 2", len(page), page)
	}

	total := readyCounterTotal(t, ctx, fixture, request)
	if total != int64(len(unbounded)) {
		t.Errorf("CountReady = %d, len(Ready(Limit=0).Items) = %d (%v): the total and the page it sizes must describe one set",
			total, len(unbounded), unbounded)
	}
}

// RunReadyCounterRejectsLimitAndOffset pins the deterministic
// request-validation refusals (issueops/readycounter.go:72-82): a Limit — set
// to any value, including an explicit zero — and a non-zero Offset are both
// ErrValidation.
//
// A bounded count would answer "how many of the first N", which is not a
// cardinality and would make the identity above false; an offset would
// subtract the rows it skipped from the size of a set that still holds them.
//
// ReadyRequest.Limit is a pointer so "unset" and "explicitly unlimited" stay
// distinguishable (issueops/reader.go:85-89), and only the first is what this
// request permits — an unlimited count is the only kind there is.
func RunReadyCounterRejectsLimitAndOffset(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcrej"
	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(fixture.IssuePrefix+"-rcrej-a", 1, label))
	if got := readyCounterTotal(t, ctx, fixture, publicops.ReadyRequest{
		Labels: []string{label}, Sort: readyCounterSort,
	}); got != 1 {
		t.Fatalf("CountReady = %d for the one row this case seeded, want 1", got)
	}

	limit := 5
	unlimited := 0
	for _, refusal := range []struct {
		name    string
		request publicops.ReadyRequest
	}{
		{"limit", publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort, Limit: &limit}},
		{"explicitly unlimited limit", publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort, Limit: &unlimited}},
		{"offset", publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort, Offset: 1}},
	} {
		if _, err := fixture.ReadyCounter.CountReady(ctx, refusal.request); !errors.Is(err, storage.ErrValidation) {
			t.Errorf("CountReady with %s: error = %v, want ErrValidation", refusal.name, err)
		}
	}
}

// RunReadyCounterCountsTheBlockerAwareSet pins the sentence that makes this a
// role of its own rather than a sixth thing Counter does
// (issueops/readycounter.go:26-30): the predicate is BLOCKER-AWARE, so a row
// with an open blocker is not in the count even though it is open, unclosed and
// otherwise indistinguishable from the row beside it.
//
// Both the listing and the literal are checked: the listing says the two agree
// on the same set, and the literal catches an identity that holds because both
// surfaces dropped the blocker predicate.
func RunReadyCounterCountsTheBlockerAwareSet(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcblock"
	blocker := fixture.IssuePrefix + "-rcblock-blocker"
	blocked := fixture.IssuePrefix + "-rcblock-blocked"
	free := fixture.IssuePrefix + "-rcblock-free"
	for _, id := range []string{blocker, blocked, free} {
		seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(id, 1, label))
	}
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: blocked, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the blocking edge: %v", err)
	}

	request := publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort}
	listed := readyCounterPageIDs(t, ctx, fixture, request, 0)
	total := readyCounterTotal(t, ctx, fixture, request)

	if total != 2 {
		t.Errorf("CountReady = %d over three open rows one of which has an open blocker, want 2", total)
	}
	if total != int64(len(listed)) {
		t.Errorf("CountReady = %d, Reader.Ready listed %d (%v)", total, len(listed), listed)
	}
	for _, id := range listed {
		if id == blocked {
			t.Errorf("Reader.Ready listed the blocked row %s, so the count above agrees with a listing that is itself wrong", blocked)
		}
	}
}

// RunReadyCounterEphemeralGateMatchesTheListing pins the wisp half of the
// identity (issueops/readycounter.go:68-70): a request that admits ephemeral
// rows counts them exactly as the listing lists them, and one that does not
// counts neither.
//
// The store-backed body sizes the ready set as one COUNT(*) per plane MINUS
// their overlap (internal/storage/issueops.CountReadyWorkInTx) while the
// listing merges the two planes row by row, so the wisp tier is where the count
// is assembled by different arithmetic than the page. Both directions of the
// gate are asserted because each fails on its own: a body that always merged
// the wisp plane over-counts a default request, one that never merged it
// under-counts an opted-in one.
func RunReadyCounterEphemeralGateMatchesTheListing(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcwisp"
	durable := fixture.IssuePrefix + "-rcwisp-d"
	wisp := fixture.IssuePrefix + "-rcwisp-w"
	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(durable, 1, label))
	seedReadyCounterWisp(t, ctx, fixture, readyCounterIssue(wisp, 1, label))

	byDefault := publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort}
	assertReadyCounterAgreesWithTheListing(t, ctx, fixture, byDefault, 1,
		"ephemeral rows are outside the default ready set")

	admitted := byDefault
	admitted.IncludeEphemeral = true
	assertReadyCounterAgreesWithTheListing(t, ctx, fixture, admitted, 2,
		"IncludeEphemeral pulls the wisp tier into both surfaces")
}

// RunReadyCounterEmptyFrontIsZeroAndNil pins the whole of this role's
// "not found" story (issueops/readycounter.go:84-87): a predicate that matches
// nothing is 0 with a NIL ERROR.
//
// The nil is the load-bearing half: an empty ready front is the steady state of
// a drained queue, and a poller that had to classify an error to read a zero
// would be pattern-matching prose. The decoy makes the zero about the FILTER
// rather than about an empty database.
func RunReadyCounterEmptyFrontIsZeroAndNil(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcempty"
	decoy := fixture.IssuePrefix + "-rcempty-decoy"
	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(decoy, 0, label+"-other"))

	result, err := fixture.ReadyCounter.CountReady(ctx, publicops.ReadyRequest{
		Labels: []string{label}, Sort: readyCounterSort,
	})
	if err != nil {
		t.Fatalf("CountReady over an empty ready front: error = %v, want nil — an empty front is a normal outcome, not an error", err)
	}
	if result.Total != 0 {
		t.Errorf("CountReady over an empty ready front = %d, want 0", result.Total)
	}
	if got := readyCounterTotal(t, ctx, fixture, publicops.ReadyRequest{
		Labels: []string{label + "-other"}, Sort: readyCounterSort,
	}); got != 1 {
		t.Errorf("CountReady for the decoy's own label = %d, want 1: the zero above must be the filter's doing, not an empty database", got)
	}
}

// RunReadyCounterWritesNothing pins issueops/readycounter.go:45-48: counting is
// a read. Nothing records a history entry, and a refused count does not either.
//
// It is asserted on the history log rather than on a row read-back because that
// is the observable both an accidental commit and an accidental write would
// move: every versioned unit of work in this tree ends in a Dolt commit, so a
// count that took a write transaction would show up here even though it changed
// no column.
func RunReadyCounterWritesNothing(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil, so the counting-is-a-read clause is unpinned on this backend")
	}
	label := fixture.IssuePrefix + "-rcread"
	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(fixture.IssuePrefix+"-rcread-a", 1, label))

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	if got := readyCounterTotal(t, ctx, fixture, publicops.ReadyRequest{
		Labels: []string{label}, Sort: readyCounterSort,
	}); got != 1 {
		t.Fatalf("CountReady = %d for the one row this case seeded, want 1", got)
	}
	refused := 1
	if _, err := fixture.ReadyCounter.CountReady(ctx, publicops.ReadyRequest{
		Labels: []string{label}, Sort: readyCounterSort, Limit: &refused,
	}); !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("CountReady with a limit: error = %v, want ErrValidation", err)
	}

	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across a count and a refusal, want no change: counting is a read", before, after)
	}
}

// RunReadyCounterDoesNotMutateTheCallerRequest is the request-snapshot
// tripwire the leaf promises in so many words (issueops/readycounter.go:39-43).
//
// The request is passed by value, so only its REFERENCE members can carry a
// mutation back: the four label slices, ExcludeTypes, MetadataFields and the
// Priority pointer. Every one is populated here with a value normalization
// would want to touch — untrimmed, duplicated, comma-joined — and the
// comparison is against a second copy built by the same function, so an
// in-place trim, dedupe, sort, alias expansion or write-through fails. The
// filter matches nothing on purpose: the promise is about the request, not the
// answer.
func RunReadyCounterDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	build := func() publicops.ReadyRequest {
		priority := 2
		return publicops.ReadyRequest{
			IssueType:      " Task ",
			Labels:         []string{fixture.IssuePrefix + "-rcsnap ", fixture.IssuePrefix + "-rcsnap "},
			LabelsAny:      []string{" " + fixture.IssuePrefix + "-rcsnap-any"},
			ExcludeLabels:  []string{fixture.IssuePrefix + "-rcsnap-not "},
			ExcludeTypes:   []string{"mr,epic", " chore "},
			MetadataFields: map[string]string{"team": "conformance"},
			HasMetadataKey: "team",
			Priority:       &priority,
			Sort:           readyCounterSort,
		}
	}
	request := build()
	want := build()

	if _, err := fixture.ReadyCounter.CountReady(ctx, request); err != nil {
		t.Fatalf("CountReady with a fully populated request: %v", err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("CountReady mutated the caller's request:\n got %+v\nwant %+v", request, want)
	}
}

// RunReadyCounterCountsOnlyTheOpenRowsItsListingLists pins the STATUS half of
// the identity this role is (issueops/readycounter.go:60-70). Ready work is
// open work — "Open only, not in_progress" (internal/workapi.BuildReadyFilter)
// — and a count that sized a wider set than the page beside it would publish
// `bd ready`'s "showing 2 of 5" over two different questions.
//
// EVERY OTHER CASE IN THIS FILE SEEDS OPEN ROWS AND NOTHING ELSE, so the status
// predicate is unreachable from all of them: drop it and every count in this
// contract still ties with its listing, because the only rows in scope were
// open to begin with. That is a fixture gap rather than a missing assertion,
// and it matters here more than at the listing — the reader contract pins the
// ready set's status decision through Reader.Ready
// (RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions), but the two store
// backends answer THIS role from a different body: one indexed COUNT(*) per
// plane over its own copy of the filter
// (internal/storage/issueops.CountReadyWorkInTx), assembled beside the listing
// rather than by it. A predicate that went missing on that side alone widens
// the total while the page it sizes stays right.
//
// THE TWO EXCLUDED ROWS ARE READ BACK RAW before anything is counted. A closed
// row and an in-progress row are both invisible to both surfaces, so "excluded"
// and "the seed never landed in that status" produce byte-identical answers; the
// raw read is the only thing that tells them apart, and without it a create that
// silently normalized either to `open` would turn this case into a slow copy of
// the identity case.
//
// WHAT THIS FIXTURE CANNOT SEE: the OR-set form of the same predicate.
// types.WorkFilter carries a Statuses member and the ready builder renders it,
// but publicops.ReadyRequest has no status field of any kind, so no
// implementation of this role can be asked for one — the arm is reachable only
// from the storage seam. That is not what this case is named for: the promise
// at THIS seam is that the count's set is the listing's set, and the singular
// status the builder always sends is the whole of what decides it here.
func RunReadyCounterCountsOnlyTheOpenRowsItsListingLists(t *testing.T, ctx context.Context, fixture ReadyCounterFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcstatus"
	open := fixture.IssuePrefix + "-rcstatus-open"
	inProgress := fixture.IssuePrefix + "-rcstatus-wip"
	closed := fixture.IssuePrefix + "-rcstatus-closed"

	seedReadyCounterIssue(t, ctx, fixture, readyCounterIssue(open, 1, label))
	wip := readyCounterIssue(inProgress, 1, label)
	wip.Status = types.StatusInProgress
	seedReadyCounterIssue(t, ctx, fixture, wip)
	done := readyCounterIssue(closed, 1, label)
	done.Status = types.StatusClosed
	seedReadyCounterIssue(t, ctx, fixture, done)

	for _, seed := range []struct {
		id   string
		want types.Status
	}{{inProgress, types.StatusInProgress}, {closed, types.StatusClosed}} {
		if got := readyCounterStoredStatus(t, ctx, fixture, seed.id); got != string(seed.want) {
			t.Fatalf("the row seeded as %s is stored with status %q; a row that is not in the status this case "+
				"excludes cannot show that the count excludes it", seed.want, got)
		}
	}

	assertReadyCounterAgreesWithTheListing(t, ctx, fixture,
		publicops.ReadyRequest{Labels: []string{label}, Sort: readyCounterSort}, 1,
		"ready work is open work: neither the in-progress row nor the closed one is in the set this role sizes")
}

// readyCounterStoredStatus reads one durable row's status column. It is the
// only raw read in this contract, and it exists because the rows a count
// EXCLUDES are invisible to both surfaces the rest of the file compares.
func readyCounterStoredStatus(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, id string) string {
	t.Helper()
	if fixture.QueryScalar == nil {
		t.Skip("fixture cannot read rows: QueryScalar is nil, so an excluded row cannot be told from an unseeded one")
	}
	var status string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &status); err != nil {
		t.Fatalf("read status for %s: %v", id, err)
	}
	return status
}

// readyCounterSort is the order every case names: the policy whose SQL is a
// plain "ORDER BY priority, created_at, id", so no case depends on when the
// fixture was seeded.
const readyCounterSort = "priority"

func readyCounterIssue(id string, priority int, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
}

// readyCounterIDs mints n ids under one case's namespace.
func readyCounterIDs(fixture ReadyCounterFixture, name string, n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("%s-%s-%d", fixture.IssuePrefix, name, i))
	}
	return ids
}

func seedReadyCounterIssue(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedReadyCounterWisp(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, issue *types.Issue) {
	t.Helper()
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

// readyCounterTotal runs one count and fails the case on an error.
func readyCounterTotal(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, request publicops.ReadyRequest) int64 {
	t.Helper()
	result, err := fixture.ReadyCounter.CountReady(ctx, request)
	if err != nil {
		t.Fatalf("CountReady(labels=%v, includeEphemeral=%v): %v", request.Labels, request.IncludeEphemeral, err)
	}
	return result.Total
}

// readyCounterPageIDs lists the same question at a given limit and returns the
// ids, so a failure names the rows rather than two numbers that differ.
func readyCounterPageIDs(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, request publicops.ReadyRequest, limit int) []string {
	t.Helper()
	paged := request
	paged.Limit = &limit
	page, err := fixture.Reader.Ready(ctx, paged)
	if err != nil {
		t.Fatalf("Reader.Ready(labels=%v, limit=%d): %v", request.Labels, limit, err)
	}
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item != nil && item.Issue != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// assertReadyCounterAgreesWithTheListing checks the identity and the literal
// together: the identity says the two surfaces agree, the literal says they
// agree on the RIGHT set.
func assertReadyCounterAgreesWithTheListing(t *testing.T, ctx context.Context, fixture ReadyCounterFixture, request publicops.ReadyRequest, want int64, because string) {
	t.Helper()
	listed := readyCounterPageIDs(t, ctx, fixture, request, 0)
	total := readyCounterTotal(t, ctx, fixture, request)
	if total != want {
		t.Errorf("CountReady(includeEphemeral=%v) = %d, want %d: %s", request.IncludeEphemeral, total, want, because)
	}
	if total != int64(len(listed)) {
		t.Errorf("CountReady(includeEphemeral=%v) = %d, Reader.Ready listed %d (%v)",
			request.IncludeEphemeral, total, len(listed), listed)
	}
}
