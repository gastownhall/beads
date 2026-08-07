package conformance

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.Counter must
// satisfy. Each case asserts what issueops/counter.go PROMISES, cited by line;
// a backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// There are three wirings — the server-backed store, the embedded store and
// the unit-of-work provider — but only two independent votes: dolt and
// embeddeddolt both hand back internal/workapi/storecounter, so they are one
// vote plus an engine check, and the unit-of-work provider is the second. All
// three build their filter
// through workapi.BuildCountFilter, so what these cases can catch below that
// builder is the EXECUTION half — which table each seam counts, how it merges
// the wisp tier, and how it names a bucket.
//
// EVERY CASE SCOPES ITSELF WITH IDFilter, listing the exact ids it seeded.
// The three fixtures share one database per suite and the two store fixtures
// share it with every other role's cases, so an unscoped count would be an
// assertion about the whole workspace and would fail the moment a sibling
// suite seeded a row.
//
// What is deliberately NOT here:
//   - the mapping from flags to a request, which is `bd count`'s job and is
//     pinned in cmd/bd/count_filter_test.go;
//   - the filter CONSTRUCTION, which all three share through one builder and
//     which internal/workapi/count_test.go pins directly, including the
//     --include-infra cardinality parity with `bd list` (GH#4387);
//   - whether Total and Groups are one snapshot, which counter.go explicitly
//     does not promise because the store-backed body cannot offer it.

// CounterFixture supplies adapter-specific storage access for the count
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type CounterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Counter     publicops.Counter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case
	// that needs it SKIPS rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunCounterCountsTheDurablePlaneByDefault pins counter.go:123-126 from the
// unset side: without IncludeInfra a count answers for the durable plane only.
//
// The wisp seeded here catches a body that dropped the SkipWisps default: the
// merge is the storage seam's DEFAULT, so "durable only" is an active decision
// this builder makes on every plain count.
func RunCounterCountsTheDurablePlaneByDefault(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	durable := fixture.IssuePrefix + "-plane-durable"
	wisp := fixture.IssuePrefix + "-plane-wisp"
	seedCounterIssue(t, ctx, fixture, counterSeed(durable))
	seedCounterWisp(t, ctx, fixture, counterSeed(wisp))

	scope := counterScope(durable, wisp)
	assertCounterTotal(t, ctx, fixture, scope, 1)
}

// RunCounterIncludeInfraMergesTheWispTier pins the first of the four things
// counter.go:108-117 promises IncludeInfra does at once: the ephemeral tier is
// merged in. It is the same two seeds the case above uses, deliberately: the
// pair of cases is one A/B on a single flag.
func RunCounterIncludeInfraMergesTheWispTier(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	durable := fixture.IssuePrefix + "-infra-durable"
	wisp := fixture.IssuePrefix + "-infra-wisp"
	seedCounterIssue(t, ctx, fixture, counterSeed(durable))
	seedCounterWisp(t, ctx, fixture, counterSeed(wisp))

	scope := counterScope(durable, wisp)
	assertCounterTotal(t, ctx, fixture, scope, 1)

	scope.IncludeInfra = true
	assertCounterTotal(t, ctx, fixture, scope, 2)
}

// RunCounterIncludeInfraExcludesGates pins the third clause of
// counter.go:115: under IncludeInfra a gate bead is excluded unless
// IssueType asks for gates by name.
//
// The DEFAULT count is asserted first and includes the gate, which is the
// asymmetry worth pinning: the exclusion belongs to the --include-infra mode
// alone, because that mode exists to match `bd list --include-infra --all`'s
// cardinality.
func RunCounterIncludeInfraExcludesGates(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	task := fixture.IssuePrefix + "-gate-task"
	gate := fixture.IssuePrefix + "-gate-gate"
	seedCounterIssue(t, ctx, fixture, counterSeed(task))
	gateSeed := counterSeed(gate)
	gateSeed.IssueType = types.IssueType("gate")
	seedCounterIssue(t, ctx, fixture, gateSeed)

	scope := counterScope(task, gate)
	assertCounterTotal(t, ctx, fixture, scope, 2)

	scope.IncludeInfra = true
	assertCounterTotal(t, ctx, fixture, scope, 1)

	// Asked for by name, the gate comes back: the exclusion is a default, not
	// a ban on counting gates.
	scope.IssueType = "gate"
	assertCounterTotal(t, ctx, fixture, scope, 1)
}

// RunCounterCountsClosedRows pins counter.go:233-235: an empty request counts
// every durable row INCLUDING closed ones, because a count applies none of the
// listing's default exclusions. A `bd list` of the same two rows shows one,
// which is what makes Counter a separate role from Reader.
func RunCounterCountsClosedRows(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	open := fixture.IssuePrefix + "-status-open"
	closed := fixture.IssuePrefix + "-status-closed"
	seedCounterIssue(t, ctx, fixture, counterSeed(open))
	closedSeed := counterSeed(closed)
	closedSeed.Status = types.StatusClosed
	seedCounterIssue(t, ctx, fixture, closedSeed)

	scope := counterScope(open, closed)
	assertCounterTotal(t, ctx, fixture, scope, 2)

	scope.Status = "closed"
	assertCounterTotal(t, ctx, fixture, scope, 1)

	// "all" is the other spelling of every status, and it must not narrow.
	scope.Status = "all"
	assertCounterTotal(t, ctx, fixture, scope, 2)
}

// RunCounterAnUnknownStatusMatchesNothing pins counter.go:50-56 and :237-241:
// an unrecognized status name matches nothing and answers 0 with a nil error,
// rather than failing. A scripted caller counting a status its workspace has
// since dropped reads a zero, and that is the shipped behavior of both front
// doors.
//
// The nil error is the load-bearing half: there is no ErrNotFound on this role.
func RunCounterAnUnknownStatusMatchesNothing(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-unknown-status"
	seedCounterIssue(t, ctx, fixture, counterSeed(anchor))

	scope := counterScope(anchor)
	assertCounterTotal(t, ctx, fixture, scope, 1)

	scope.Status = "no-such-status"
	assertCounterTotal(t, ctx, fixture, scope, 0)
}

// RunCounterGroupsPartitionTheScalarSet pins counter.go:168-169 and :244-245
// for a dimension whose buckets do NOT overlap: the grouped Total is the same
// number a scalar Count returns, and the buckets partition it exactly.
//
// Both halves are needed. The Total equality catches a grouped path that built
// a different filter than the scalar one; the partition catches a grouped
// query that dropped or double-counted rows while still reporting a total
// computed by the scalar query, which is the shape the total's independence
// makes possible.
func RunCounterGroupsPartitionTheScalarSet(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-bucket-open-a"
	second := fixture.IssuePrefix + "-bucket-open-b"
	third := fixture.IssuePrefix + "-bucket-closed"
	seedCounterIssue(t, ctx, fixture, counterSeed(first))
	seedCounterIssue(t, ctx, fixture, counterSeed(second))
	closedSeed := counterSeed(third)
	closedSeed.Status = types.StatusClosed
	seedCounterIssue(t, ctx, fixture, closedSeed)

	scope := counterScope(first, second, third)
	scalar := counterTotal(t, ctx, fixture, scope)
	if scalar != 3 {
		t.Fatalf("scalar count = %d, want the 3 rows this case seeded", scalar)
	}

	result := counterGroups(t, ctx, fixture, scope, publicops.CountGroupStatus)
	if result.Total != scalar {
		t.Errorf("grouped Total = %d, scalar Count = %d: the two must answer for the same set", result.Total, scalar)
	}
	assertCounterBuckets(t, result.Groups, map[string]int{"open": 2, "closed": 1})

	sum := 0
	for _, count := range result.Groups {
		sum += count
	}
	if int64(sum) != result.Total {
		t.Errorf("status buckets sum to %d, Total = %d: status buckets do not overlap, so they must partition the set", sum, result.Total)
	}
}

// RunCounterLabelBucketsOverlapSoTotalIsNotTheirSum pins the clause that makes
// CountByGroupResult.Total a field rather than something a caller adds up
// (counter.go:171-177): label buckets OVERLAP, so an issue carrying two labels
// is one row in Total and one row in each of two buckets.
//
// This is the one dimension where a caller summing the buckets reports a
// workspace larger than it is, and it is why the role computes the total with
// a scalar count instead of a SUM. The "(no labels)" bucket is asserted here
// too because a body that dropped it would leave the buckets summing to LESS
// than the total, which is the mirror-image failure.
func RunCounterLabelBucketsOverlapSoTotalIsNotTheirSum(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	tagged := fixture.IssuePrefix + "-label-tagged"
	bare := fixture.IssuePrefix + "-label-bare"
	taggedSeed := counterSeed(tagged)
	taggedSeed.Labels = []string{fixture.IssuePrefix + "-alpha", fixture.IssuePrefix + "-beta"}
	seedCounterIssue(t, ctx, fixture, taggedSeed)
	seedCounterIssue(t, ctx, fixture, counterSeed(bare))

	scope := counterScope(tagged, bare)
	result := counterGroups(t, ctx, fixture, scope, publicops.CountGroupLabel)

	if result.Total != 2 {
		t.Errorf("Total = %d, want 2: the total is the scalar count of the matching set, not the sum of overlapping buckets", result.Total)
	}
	assertCounterBuckets(t, result.Groups, map[string]int{
		fixture.IssuePrefix + "-alpha": 1,
		fixture.IssuePrefix + "-beta":  1,
		"(no labels)":                  1,
	})
}

// RunCounterNamesTheEmptyBuckets pins the two synthesized keys
// CountByGroupResult.Groups promises (counter.go:159-161): unassigned rows
// bucket under "(unassigned)", never under the empty string. The column holds
// an empty string for an unassigned row, so a body that skipped the
// normalization produces a map with a "" key.
func RunCounterNamesTheEmptyBuckets(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	assigned := fixture.IssuePrefix + "-assignee-taken"
	unassigned := fixture.IssuePrefix + "-assignee-none"
	assignedSeed := counterSeed(assigned)
	assignedSeed.Assignee = fixture.IssuePrefix + "-alice"
	seedCounterIssue(t, ctx, fixture, assignedSeed)
	seedCounterIssue(t, ctx, fixture, counterSeed(unassigned))

	scope := counterScope(assigned, unassigned)
	result := counterGroups(t, ctx, fixture, scope, publicops.CountGroupAssignee)
	assertCounterBuckets(t, result.Groups, map[string]int{
		fixture.IssuePrefix + "-alice": 1,
		"(unassigned)":                 1,
	})
}

// RunCounterPrefixesPriorityBuckets pins the third normalization
// CountByGroupResult.Groups promises (counter.go:157-158): a priority bucket
// is "P" followed by the number. It is the one bucket whose underlying column
// is an integer, so it is the one a body reaches through a CAST — the step
// where a backend can come back with "2" where another comes back with "P2".
func RunCounterPrefixesPriorityBuckets(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	high := fixture.IssuePrefix + "-priority-high"
	low := fixture.IssuePrefix + "-priority-low"
	highSeed := counterSeed(high)
	highSeed.Priority = 1
	seedCounterIssue(t, ctx, fixture, highSeed)
	lowSeed := counterSeed(low)
	lowSeed.Priority = 3
	seedCounterIssue(t, ctx, fixture, lowSeed)

	scope := counterScope(high, low)
	result := counterGroups(t, ctx, fixture, scope, publicops.CountGroupPriority)
	assertCounterBuckets(t, result.Groups, map[string]int{"P1": 1, "P3": 1})
}

// RunCounterRefusesAnUnknownGroup pins counter.go:145-148 and :247: the
// CountGroup set is closed, and a value outside it is ErrValidation rather
// than an empty answer.
//
// The empty GroupBy is included because "no dimension" is not a scalar count
// in disguise. The refusal is TYPED so both front doors classify it with
// errors.Is instead of matching on the seam's "unsupported groupBy" prose.
func RunCounterRefusesAnUnknownGroup(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-badgroup"
	seedCounterIssue(t, ctx, fixture, counterSeed(anchor))
	scope := counterScope(anchor)

	for _, group := range []publicops.CountGroup{"", "owner", "Status", "label "} {
		_, err := fixture.Counter.CountByGroup(ctx, publicops.CountByGroupRequest{
			Filter: scope, GroupBy: group,
		})
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("CountByGroup(%q) error = %v, want ErrValidation", group, err)
		}
	}
}

// RunCounterNormalizesLabelsAndLeavesTheRequestAlone pins the two halves of
// counter.go:76-80 and :217-221 that meet on the same field: label entries are
// trimmed and de-duplicated INSIDE, and the caller's slice is not written
// through on the way. Normalization is exactly the step that would write
// through the caller's slices, and reusing one request for several counts is
// the ordinary way to use this role.
func RunCounterNormalizesLabelsAndLeavesTheRequestAlone(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	tagged := fixture.IssuePrefix + "-normalize"
	label := fixture.IssuePrefix + "-tag"
	seed := counterSeed(tagged)
	seed.Labels = []string{label}
	seedCounterIssue(t, ctx, fixture, seed)

	request := counterScope(tagged)
	request.Labels = []string{"  " + label + "  ", label, ""}
	snapshot := append([]string(nil), request.Labels...)
	ids := request.IDFilter

	assertCounterTotal(t, ctx, fixture, request, 1)

	if len(request.Labels) != len(snapshot) {
		t.Fatalf("the caller's Labels became %v, want them left as %v", request.Labels, snapshot)
	}
	for i, want := range snapshot {
		if request.Labels[i] != want {
			t.Errorf("the caller's Labels[%d] became %q, want %q", i, request.Labels[i], want)
		}
	}
	if request.IDFilter != ids {
		t.Errorf("the caller's IDFilter became %q, want %q", request.IDFilter, ids)
	}
}

// RunCounterWritesNothing pins counter.go:223-226: counting is a read. Nothing
// records a history entry, and a refused count does not either.
//
// It is asserted on the history log rather than on a row read-back because
// every versioned unit of work in this tree ends in a Dolt commit, so a count
// that took a write transaction shows up here even when it changed no column.
func RunCounterWritesNothing(t *testing.T, ctx context.Context, fixture CounterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-readonly"
	seedCounterIssue(t, ctx, fixture, counterSeed(anchor))
	scope := counterScope(anchor)
	before := counterHistoryCount(t, ctx, fixture)

	assertCounterTotal(t, ctx, fixture, scope, 1)
	counterGroups(t, ctx, fixture, scope, publicops.CountGroupStatus)
	if _, err := fixture.Counter.CountByGroup(ctx, publicops.CountByGroupRequest{
		Filter: scope, GroupBy: "no-such-dimension",
	}); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("CountByGroup with an unknown dimension error = %v, want ErrValidation", err)
	}

	if after := counterHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d across two counts and a refusal, want no change: counting is a read", before, after)
	}
}

// counterScope is the request every case starts from: a predicate narrowed to
// exactly the ids that case seeded.
func counterScope(ids ...string) publicops.CountRequest {
	filter := ""
	for i, id := range ids {
		if i > 0 {
			filter += ","
		}
		filter += id
	}
	return publicops.CountRequest{IDFilter: filter}
}

func counterSeed(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

func seedCounterIssue(t *testing.T, ctx context.Context, fixture CounterFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedCounterWisp(t *testing.T, ctx context.Context, fixture CounterFixture, issue *types.Issue) {
	t.Helper()
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

// counterTotal runs one scalar count and fails the case on an error.
func counterTotal(t *testing.T, ctx context.Context, fixture CounterFixture, request publicops.CountRequest) int64 {
	t.Helper()
	result, err := fixture.Counter.Count(ctx, request)
	if err != nil {
		t.Fatalf("Count(%+v): %v", request, err)
	}
	return result.Total
}

func assertCounterTotal(t *testing.T, ctx context.Context, fixture CounterFixture, request publicops.CountRequest, want int64) {
	t.Helper()
	if got := counterTotal(t, ctx, fixture, request); got != want {
		t.Errorf("Count(%+v) = %d, want %d", request, got, want)
	}
}

func counterGroups(t *testing.T, ctx context.Context, fixture CounterFixture, request publicops.CountRequest, group publicops.CountGroup) publicops.CountByGroupResult {
	t.Helper()
	result, err := fixture.Counter.CountByGroup(ctx, publicops.CountByGroupRequest{
		Filter: request, GroupBy: group,
	})
	if err != nil {
		t.Fatalf("CountByGroup(%s): %v", group, err)
	}
	if result.Groups == nil {
		t.Fatalf("CountByGroup(%s) returned a nil map, want an empty one: a dimension with no rows is an empty map", group)
	}
	return result
}

// assertCounterBuckets compares the whole map rather than the keys a case
// happens to care about, so an EXTRA bucket fails too. That is the half that
// catches a body whose predicate leaked — a bucket for a row the scope never
// admitted is the same defect as a missing one, and a key-by-key check would
// pass it.
func assertCounterBuckets(t *testing.T, got map[string]int, want map[string]int) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("buckets = %v, want %v", got, want)
	}
}

// counterHistoryCount reads the branch's history depth, or SKIPS the case when
// the backend cannot observe history at all. A silent pass would leave the
// read-only clause looking pinned on a backend that never checked it.
func counterHistoryCount(t *testing.T, ctx context.Context, fixture CounterFixture) int {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil, so the counting-is-a-read clause is unpinned on this backend")
	}
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	return entries
}
