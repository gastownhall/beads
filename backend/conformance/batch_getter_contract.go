package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.BatchGetter
// must satisfy. Each case asserts what issueops/batchgetter.go PROMISES, cited
// by symbol; a backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// THREE LEGS, ONE BODY, GraphCounterFixture's reason: all three reach the same
// storage/issueops.ExecuteGetMany, which itself reaches GetIssuesByIDsInTx for
// the actual read. The two stores wrap it in their own read transaction, and
// the unit-of-work provider reaches it through the domain repository, whose
// runner publishes exactly the DBTX method set that function takes. So a
// three-leg run here is ONE READING plus two engine checks and three wrapper
// checks, and the cases are written for that: they assert the result's shape
// and order rather than message text, so a wrapper that loses the request,
// drops ordering or breaks errors.Is is what a per-leg failure would actually
// be.
//
// EVERY CASE NAMES THE EXACT IDS IT SEEDED, GraphCounterFixture's reason: the
// three fixtures share one database per suite and the two store fixtures share
// it with every other role's cases.
type BatchGetterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	BatchGetter publicops.BatchGetter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunBatchGetterFindsRequestedIssues pins the found half of GetManyResult: an
// id a GetManyRequest names and the store holds comes back hydrated, with the
// id that resolved it.
func RunBatchGetterFindsRequestedIssues(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-found-a"
	second := fixture.IssuePrefix + "-found-b"
	seedBatchGetterIssues(t, ctx, fixture, first, second)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{first, second}})
	assertBatchGetterIssueIDs(t, result, first, second)
	assertBatchGetterMissingIDs(t, result)
}

// RunBatchGetterReportsMissingIDs pins GetManyResult.Missing: an id naming no
// stored row is reported there, beside whatever else resolved, rather than
// refusing the whole call. The ghost sits BETWEEN two real ids, the same
// placement RunGraphCounterDistinguishesNoEdgesFromNoAnchor uses, so a body
// that short-circuited on the first miss cannot pass by accident.
func RunBatchGetterReportsMissingIDs(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-missing-real1"
	ghost := fixture.IssuePrefix + "-missing-ghost"
	second := fixture.IssuePrefix + "-missing-real2"
	seedBatchGetterIssues(t, ctx, fixture, first, second)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{first, ghost, second}})
	assertBatchGetterIssueIDs(t, result, first, second)
	assertBatchGetterMissingIDs(t, result, ghost)
}

// RunBatchGetterCollapsesRepeatedIDs pins GetManyRequest.IDs's repeats clause:
// an id named twice is one entry, at the position of its first mention. The
// repeat is placed AFTER a different id, RunGraphCounterCollapsesRepeatedAnchors'
// reason: a body that de-duplicated by sorting rather than by first mention
// would answer b, a instead of a, b.
func RunBatchGetterCollapsesRepeatedIDs(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-dup-b"
	second := fixture.IssuePrefix + "-dup-a"
	seedBatchGetterIssues(t, ctx, fixture, first, second)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{
		IDs: []string{first, second, first, second, first},
	})
	assertBatchGetterIssueIDs(t, result, first, second)
}

// RunBatchGetterAnswersInRequestOrder pins GetManyResult.Issues' ordering
// clause: the answer comes back in the REQUEST's order, never the storage
// engine's natural order. The ids are seeded in the opposite order from the one
// requested, so a body that forwarded the storage engine's order would fail.
func RunBatchGetterAnswersInRequestOrder(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-order-c"
	second := fixture.IssuePrefix + "-order-b"
	third := fixture.IssuePrefix + "-order-a"
	// Seeded in storage order third, second, first — the reverse of the
	// request below — so a body that forwarded insertion order would fail.
	seedBatchGetterIssues(t, ctx, fixture, third, second, first)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{first, second, third}})
	assertBatchGetterIssueIDs(t, result, first, second, third)
}

// RunBatchGetterResolvesIDsExactly pins the exactness GetManyRequest.IDs
// documents: a prefix of a real id and a real id padded with whitespace are
// both reported Missing rather than resolved, the same exactness
// RunGraphCounterResolvesIDsExactly pins for EdgeCountRequest.IDs.
func RunBatchGetterResolvesIDsExactly(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	real := fixture.IssuePrefix + "-exact-real"
	seedBatchGetterIssues(t, ctx, fixture, real)

	prefix := real[:len(real)-2]
	spaced := " " + real + " "
	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{real, prefix, spaced}})
	assertBatchGetterIssueIDs(t, result, real)
	assertBatchGetterMissingIDs(t, result, prefix, spaced)
}

// RunBatchGetterAnswersAnEmptyRequest pins GetManyRequest.IDs's empty-slice
// clause: no ids is not an error, it is an answer with no issues — and both
// slices are never nil, so a front door that marshals them emits [] rather
// than null.
func RunBatchGetterAnswersAnEmptyRequest(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	result, err := fixture.BatchGetter.GetMany(ctx, publicops.GetManyRequest{})
	if err != nil {
		t.Fatalf("GetMany with no ids = %v, want an empty answer", err)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("GetMany with no ids returned %d issues, want none", len(result.Issues))
	}
	if result.Issues == nil {
		t.Error("Issues is nil; the contract promises it is never nil for a successful call")
	}
	if result.Missing == nil {
		t.Error("Missing is nil; the contract promises it is never nil for a successful call")
	}
}

// RunBatchGetterRefusesAnUnusableRequest pins the request vocabulary
// ValidateGetManyRequest applies: an empty-string id is refused with
// ErrValidation.
func RunBatchGetterRefusesAnUnusableRequest(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	real := fixture.IssuePrefix + "-refuse-real"
	seedBatchGetterIssues(t, ctx, fixture, real)

	_, err := fixture.BatchGetter.GetMany(ctx, publicops.GetManyRequest{IDs: []string{real, ""}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("GetMany with an empty id beside a real one = %v, want ErrValidation", err)
	}
}

// RunBatchGetterRefusesOverTheCap pins MaxGetManyIDs: a request naming more
// ids than the cap is refused with a *publicops.TooManyIDsError that wraps
// ErrValidation, counted on the request as sent, BEFORE deduplication — the
// request below repeats one id past the cap, so a body that checked the
// deduplicated count would wrongly let it through.
func RunBatchGetterRefusesOverTheCap(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	real := fixture.IssuePrefix + "-cap-real"
	seedBatchGetterIssues(t, ctx, fixture, real)

	ids := make([]string, publicops.MaxGetManyIDs+1)
	for i := range ids {
		ids[i] = real
	}
	_, err := fixture.BatchGetter.GetMany(ctx, publicops.GetManyRequest{IDs: ids})
	var tooMany *publicops.TooManyIDsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("GetMany over the cap = %v, want *publicops.TooManyIDsError", err)
	}
	if tooMany.Requested != len(ids) {
		t.Errorf("TooManyIDsError.Requested = %d, want %d (the count before deduplication)", tooMany.Requested, len(ids))
	}
	if tooMany.Cap != publicops.MaxGetManyIDs {
		t.Errorf("TooManyIDsError.Cap = %d, want %d", tooMany.Cap, publicops.MaxGetManyIDs)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("GetMany over the cap = %v, want it to also match ErrValidation", err)
	}
}

// RunBatchGetterAcceptsExactlyTheCap pins the boundary MaxGetManyIDs draws:
// a request naming EXACTLY the cap's worth of distinct ids is accepted and
// answered in full, not refused. RunBatchGetterRefusesOverTheCap pins the
// cap+1 side of this line; without this case a `>` to `>=` mutation on the
// comparison would still refuse a one-over request (cap+1 is still > cap-1)
// and pass that case while silently refusing every legitimate cap-sized
// request.
func RunBatchGetterAcceptsExactlyTheCap(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	ids := make([]string, publicops.MaxGetManyIDs)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-atcap-%d", fixture.IssuePrefix, i)
	}
	seedBatchGetterIssues(t, ctx, fixture, ids...)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: ids})
	if len(result.Issues) != publicops.MaxGetManyIDs {
		t.Fatalf("GetMany with exactly MaxGetManyIDs (%d) ids returned %d issues, want %d",
			publicops.MaxGetManyIDs, len(result.Issues), publicops.MaxGetManyIDs)
	}
	assertBatchGetterMissingIDs(t, result)
}

// RunBatchGetterHydratesLabels pins that a resolved issue's Labels come back
// populated: hydration for this role is LABELS ONLY (no dependencies,
// dependents or comments — issueops.GetManyResult.Issues documents the
// scope), so Labels is the one piece of relational data a regression here
// could silently drop without any other case noticing.
func RunBatchGetterHydratesLabels(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-labels-a"
	seed := batchGetterSeed(id, false)
	seed.Labels = []string{"alpha", "beta"}
	if err := fixture.CreateIssue(ctx, seed, "batch-getter-seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{id}})
	assertBatchGetterIssueIDs(t, result, id)
	got := result.Issues[0].Labels
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("GetMany(%s).Labels = %v, want %v", id, got, want)
	}
	seen := make(map[string]bool, len(got))
	for _, l := range got {
		seen[l] = true
	}
	for _, l := range want {
		if !seen[l] {
			t.Errorf("GetMany(%s).Labels = %v, want it to include %q", id, got, l)
		}
	}
}

// RunBatchGetterLeavesTheRequestAlone pins the no-mutation clause on
// BatchGetter: IDs is the one member a body could write through to the caller
// — de-duplication is exactly the step that would — and the contract says a
// body de-duplicates into its own copy.
func RunBatchGetterLeavesTheRequestAlone(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-immutable-a"
	second := fixture.IssuePrefix + "-immutable-b"
	seedBatchGetterIssues(t, ctx, fixture, first, second)

	ids := []string{first, first, second}
	getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: ids})

	if len(ids) != 3 || ids[0] != first || ids[1] != first || ids[2] != second {
		t.Errorf("the request's IDs slice is now %v; the contract says a body de-duplicates into its own copy", ids)
	}
}

// RunBatchGetterSharesOneReadStructurally pins the STRUCTURAL half of the
// one-snapshot clause on BatchGetter.GetMany: the existence check and the
// hydration are one call answering one found id and one missing id together
// against a STATIC fixture, which is consistent with — but does not by
// itself PROVE — the two sharing one transaction under a concurrent writer.
// A body that split the probe from the hydration into two round trips could
// still pass this case against a fixture nothing else is mutating, so it
// checks both halves of one GetManyResult instead of asserting on two
// separate calls the way RunBatchGetterFindsRequestedIssues and
// RunBatchGetterReportsMissingIDs do, but it is NOT a concurrency test: no
// writer races the read here.
//
// Renamed from RunBatchGetterReadsOneSnapshot, which overclaimed an
// atomicity guarantee this case alone does not empirically demonstrate. A
// real interleaving test — a concurrent writer racing GetMany's transaction
// so the case can show a found id and a racing create or delete resolve
// consistently with ONE snapshot boundary — needs a synchronization hook
// (e.g. "pause this leg's read transaction after its query, before it
// returns, so a second goroutine can write and commit") that roleFixtureKit
// does not expose today; that kit is FROZEN (see role_fixture_kit_test.go)
// and a role slice does not edit it. Adding the hook is tracked as follow-up
// work rather than done here, so this case stays honestly structural.
func RunBatchGetterSharesOneReadStructurally(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	present := fixture.IssuePrefix + "-snapshot-present"
	ghost := fixture.IssuePrefix + "-snapshot-ghost"
	seedBatchGetterIssues(t, ctx, fixture, present)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{present, ghost}})
	if len(result.Issues) != 1 || len(result.Missing) != 1 {
		t.Fatalf("GetMany([%s, %s]) = %d issues, %d missing; want exactly one of each from the same read",
			present, ghost, len(result.Issues), len(result.Missing))
	}
	assertBatchGetterIssueIDs(t, result, present)
	assertBatchGetterMissingIDs(t, result, ghost)
}

// RunBatchGetterCrossesBothPlanes pins that GetMany resolves ids regardless of
// which dependency plane stores them: a durable issue and an ephemeral wisp
// named in the same request both come back hydrated.
func RunBatchGetterCrossesBothPlanes(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	issue := fixture.IssuePrefix + "-plane-issue"
	wisp := fixture.IssuePrefix + "-plane-wisp"
	seedBatchGetterIssues(t, ctx, fixture, issue)
	seedBatchGetterWisp(t, ctx, fixture, wisp)

	result := getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{issue, wisp}})
	assertBatchGetterIssueIDs(t, result, issue, wisp)
}

// RunBatchGetterWritesNothing pins BatchGetter's read clause: a GetMany
// records no history entry. The delta is taken around the calls rather than as
// an absolute count, RunGraphCounterWritesNothing's reason: the seeds above it
// are versioned writes of their own.
func RunBatchGetterWritesNothing(t *testing.T, ctx context.Context, fixture BatchGetterFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the writes-nothing clause cannot be checked here")
	}
	real := fixture.IssuePrefix + "-quiet-real"
	ghost := fixture.IssuePrefix + "-quiet-ghost"
	seedBatchGetterIssues(t, ctx, fixture, real)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	getMany(t, ctx, fixture, publicops.GetManyRequest{IDs: []string{real, ghost}})
	// A refusal changes nothing either, so the same delta covers both.
	_, _ = fixture.BatchGetter.GetMany(ctx, publicops.GetManyRequest{IDs: []string{""}})
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries went %d -> %d across two GetMany calls, want no change", before, after)
	}
}

func getMany(t *testing.T, ctx context.Context, fixture BatchGetterFixture, request publicops.GetManyRequest) publicops.GetManyResult {
	t.Helper()
	result, err := fixture.BatchGetter.GetMany(ctx, request)
	if err != nil {
		t.Fatalf("GetMany(%v): %v", request.IDs, err)
	}
	return result
}

func seedBatchGetterIssues(t *testing.T, ctx context.Context, fixture BatchGetterFixture, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := fixture.CreateIssue(ctx, batchGetterSeed(id, false), "batch-getter-seed"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
}

func seedBatchGetterWisp(t *testing.T, ctx context.Context, fixture BatchGetterFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, batchGetterSeed(id, true), "batch-getter-seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func batchGetterSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func batchGetterIssueIDs(result publicops.GetManyResult) []string {
	out := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		out = append(out, issue.ID)
	}
	return out
}

func assertBatchGetterIssueIDs(t *testing.T, result publicops.GetManyResult, want ...string) {
	t.Helper()
	got := batchGetterIssueIDs(result)
	if len(got) != len(want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("issues = %v, want %v", got, want)
		}
	}
}

func assertBatchGetterMissingIDs(t *testing.T, result publicops.GetManyResult, want ...string) {
	t.Helper()
	got := result.Missing
	if len(got) != len(want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing = %v, want %v", got, want)
		}
	}
	if got == nil {
		t.Error("Missing is nil; the contract promises it is never nil for a successful call")
	}
}
