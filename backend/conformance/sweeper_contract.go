package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Sweeper must satisfy. Each case asserts what issueops/sweeper.go
// PROMISES, cited by line, rather than what any one backend happens to do; a
// backend that genuinely disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// THERE ARE TWO BODIES BEHIND THE THREE WIRINGS. dolt and embeddeddolt share
// internal/storage/issueops.SweepInTx and differ only in how they reach a
// transaction; the unit-of-work provider goes through the domain use cases and
// is genuinely separate code. So the wirings are one vote plus an engine check,
// and a second independent vote — and the selection half of both votes runs
// through the SAME internal/workapi functions. The cases below therefore spend
// their assertions on what only a real backend can show: which rows are gone,
// what a dry run left alone, what the version-control plane recorded.
//
// EVERY CASE SCOPES ITSELF WITH AN ID PATTERN, which is a precondition rather
// than hygiene. A sweep is asked of a whole TIER, so a case that swept without
// a pattern would delete the rows the next case seeded — and on the ephemeral
// tier that is a legal unfiltered request the role does not refuse.
//
// EVERY CASE PINS closed_at EXPLICITLY. The recheck the role performs is
// against a cutoff, and a seeded row whose closed_at defaulted to "now" would
// make "closed before now" a race with the test's own clock.

// SweeperFixture supplies adapter-specific storage access for the
// bulk-clearance assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type SweeperFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Sweeper is the surface under test.
	Sweeper publicops.Sweeper
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// QueryScalar runs a single-row query and scans it. It is how these cases
	// observe whether the rows are really gone.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case
	// that needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
	// AddComment attaches a comment to an issue OR a wisp. It is filled from
	// the backend's Commenter role, which resolves the plane itself, so a case
	// can cite a candidate from a wisp's comment without knowing how that
	// backend reaches wisp_comments.
	AddComment func(ctx context.Context, issueID, author, text string) error
}

// sweeperClosedAt is the stamp every seeded candidate carries, and
// sweeperCutoff is an instant strictly after it. Fixed rather than offsets from
// time.Now so the cutoff cases do not race the clock.
var (
	sweeperClosedAt = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sweeperCutoff   = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
)

// RunSweeperRefusesAnUnfilteredDurableSweep pins the safety invariant the role
// took off the CLI (issueops/sweeper.go:59-73): a durable sweep with neither a
// cutoff nor a pattern is ErrValidation, asserted at the role rather than at a
// handler so a second front door inherits it.
//
// It asserts the refusal AND its effect: a guard that returned an error after
// deleting would satisfy an errors.Is assertion perfectly.
func RunSweeperRefusesAnUnfilteredDurableSweep(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	id := sweeperSeedClosedIssue(t, ctx, fixture, "gate", false)

	result, err := fixture.Sweeper.Sweep(ctx, publicops.SweepRequest{Tier: publicops.SweepDurable})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("Sweep(durable, no cutoff, no pattern) error = %v, want ErrValidation: "+
			"the require-a-filter guard is a safety invariant of the ROLE, so a second front door inherits it", err)
	}
	if result.Swept != 0 {
		t.Errorf("refused sweep reported Swept = %d, want 0", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 1, id)

	// The ephemeral tier carries no such gate: an unfiltered sweep of closed
	// wisps is the ordinary use of `bd purge`. Asked with a pattern that matches
	// nothing so the case does not clear another case's seeds.
	if _, err := fixture.Sweeper.Sweep(ctx, publicops.SweepRequest{
		Tier:      publicops.SweepEphemeral,
		IDPattern: fixture.IssuePrefix + "-gate-nothing-*",
	}); err != nil {
		t.Fatalf("Sweep(ephemeral) error = %v, want nil: the gate is the durable tier's, not every tier's", err)
	}
}

// RunSweeperRefusesAMalformedRequest pins the other two ErrValidation clauses
// (issueops/sweeper.go): an unset or unrecognized Tier, and an IDPattern that
// is not a well-formed glob.
//
// The pattern case is a DEFECT FIX: both front doors used to discard
// filepath.Match's error, so `--pattern '['` reported "nothing to prune",
// indistinguishable from a correct pattern over an empty set.
func RunSweeperRefusesAMalformedRequest(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	for _, test := range []struct {
		name    string
		request publicops.SweepRequest
	}{
		{"unset tier", publicops.SweepRequest{IDPattern: "*"}},
		{"unrecognized tier", publicops.SweepRequest{Tier: publicops.SweepTier("wisps"), IDPattern: "*"}},
		{"malformed pattern", publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: "[bad"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fixture.Sweeper.Sweep(ctx, test.request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("Sweep(%s) error = %v, want ErrValidation", test.name, err)
			}
		})
	}
}

// RunSweeperClearsOneTierAndLeavesTheOther pins the disjointness that makes
// `bd purge` and `bd prune` ONE capability with a tier parameter
// (issueops/sweeper.go, SweepTier). Both planes are seeded with ids the SAME
// pattern admits, so a body that forgot the tier deletes the wrong rows rather
// than merely reporting a wrong number.
func RunSweeperClearsOneTierAndLeavesTheOther(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	issueID := sweeperSeedClosedIssue(t, ctx, fixture, "tier", false)
	wispID := sweeperSeedClosedIssue(t, ctx, fixture, "tier", true)

	result := sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: sweeperPattern(fixture, "tier"),
	})
	if result.Swept != 1 {
		t.Fatalf("durable sweep reported Swept = %d, want 1", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, issueID)
	sweeperAssertWispRows(t, ctx, fixture, 1, wispID)

	result = sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:      publicops.SweepEphemeral,
		IDPattern: sweeperPattern(fixture, "tier"),
	})
	if result.Swept != 1 {
		t.Fatalf("ephemeral sweep reported Swept = %d, want 1", result.Swept)
	}
	sweeperAssertWispRows(t, ctx, fixture, 0, wispID)
}

// RunSweeperProtectsPinnedRows pins the protection no request field overrides
// (issueops.SweepSkips.Pinned): a pinned closed row is held back and counted,
// and the sweep still clears everything beside it.
func RunSweeperProtectsPinnedRows(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	plain := sweeperSeedClosedIssue(t, ctx, fixture, "pin", false)
	pinned := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "pin", "keep", false), func(issue *types.Issue) {
		issue.Pinned = true
	})

	result := sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: sweeperPattern(fixture, "pin"),
	})

	if result.Swept != 1 {
		t.Errorf("Swept = %d, want 1 — the pinned row must not be one of them", result.Swept)
	}
	if result.Skipped.Pinned != 1 {
		t.Errorf("Skipped.Pinned = %d, want 1", result.Skipped.Pinned)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, plain)
	sweeperAssertIssueRows(t, ctx, fixture, 1, pinned)
}

// RunSweeperHonorsTheCutoffAndThePattern pins the two narrowing fields.
//
// The CUTOFF is HALF-OPEN: a row closed exactly at the cutoff is KEPT
// (issueops.SweepRequest.ClosedBefore, "strictly before"). An off-by-one there
// deletes a bead closed the instant a scheduled sweep names.
//
// The PATTERN is matched in Go rather than translated to SQL, so the case
// includes a character class — the construct a LIKE translation would get
// wrong, on the operation where getting it wrong deletes rows.
func RunSweeperHonorsTheCutoffAndThePattern(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	old := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "when", "old", false), nil)
	atCutoff := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "when", "edge", false), func(issue *types.Issue) {
		at := sweeperCutoff
		issue.ClosedAt = &at
	})

	cutoff := sweeperCutoff
	result := sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:         publicops.SweepDurable,
		ClosedBefore: &cutoff,
		IDPattern:    sweeperPattern(fixture, "when"),
	})
	if result.Swept != 1 {
		t.Errorf("Swept = %d, want 1 — ClosedBefore is strictly before, so the row closed AT the cutoff stays", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, old)
	sweeperAssertIssueRows(t, ctx, fixture, 1, atCutoff)

	// A character class, and a row the class excludes.
	inClass := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "glob", "a1", false), nil)
	outOfClass := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "glob", "z9", false), nil)
	result = sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: fixture.IssuePrefix + "-glob-[a-c]*",
	})
	if result.Swept != 1 {
		t.Errorf("Swept = %d, want 1 for the character-class pattern", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, inClass)
	sweeperAssertIssueRows(t, ctx, fixture, 1, outOfClass)
}

// RunSweeperDryRunChangesNothing pins issueops.Sweeper.Sweep's "A DRY RUN
// CHANGES NOTHING": the preview reports the same counts the real sweep goes on
// to report, and leaves every row where it was. The two are compared against
// EACH OTHER rather than against a literal, because that is the property a
// caller relies on.
func RunSweeperDryRunChangesNothing(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	ids := []string{
		sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "dry", "1", false), nil),
		sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "dry", "2", false), nil),
	}
	request := publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: sweeperPattern(fixture, "dry"),
	}

	preview := sweeperSweep(t, ctx, fixture, sweeperWithDryRun(request, true))
	if !preview.DryRun {
		t.Error("preview.DryRun = false; the result must echo the request it answered")
	}
	if preview.Swept != 2 {
		t.Fatalf("preview Swept = %d, want 2", preview.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 2, ids...)

	// A second preview: a dry run that had deleted anything reports a smaller
	// number here.
	if again := sweeperSweep(t, ctx, fixture, sweeperWithDryRun(request, true)); again.Swept != preview.Swept {
		t.Fatalf("second preview Swept = %d, first = %d: the first preview changed the store", again.Swept, preview.Swept)
	}

	real := sweeperSweep(t, ctx, fixture, request)
	if real.Swept != preview.Swept {
		t.Errorf("real Swept = %d, preview said %d: a preview that disagrees with its run is worse than none",
			real.Swept, preview.Swept)
	}
	if real.DryRun {
		t.Error("real.DryRun = true")
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, ids...)
}

// RunSweeperProtectsRowsCitedFromAWispComment is the quadrant the protection
// scan was blind in.
//
// ProtectReferenced promises the scan covers every not-done row's description,
// notes AND comments, and the not-done set spans both planes. Three of those
// four combinations had cases; a citation living in a comment on an open WISP
// had none, and the two bodies read comments through different code — the
// store-backed one partitions between `comments` and `wisp_comments`, the
// unit-of-work one went through a use case that reads the durable table only.
// So `bd prune` deleted the cited bead on one route and kept it on the other,
// with all three legs green.
//
// The citation is the row's ONLY one, and it is on a wisp that is OPEN, so
// nothing but a both-planes comment scan can protect it.
func RunSweeperProtectsRowsCitedFromAWispComment(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	if fixture.AddComment == nil {
		t.Skip("fixture cannot attach comments")
	}
	cited := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "wcx", "cited", false), nil)

	witness := sweeperIssue(fixture, "wcx", "live", true)
	witness.Status = types.StatusOpen
	witness.ClosedAt = nil
	witnessID := sweeperSeedOpen(t, ctx, fixture, witness)
	if err := fixture.AddComment(ctx, witnessID, "sweeper-seed",
		fmt.Sprintf("decision trail: see %s", cited)); err != nil {
		t.Fatalf("commenting on the wisp: %v", err)
	}

	request := publicops.SweepRequest{
		Tier:              publicops.SweepDurable,
		IDPattern:         sweeperPattern(fixture, "wcx"),
		ProtectReferenced: true,
	}
	result := sweeperSweep(t, ctx, fixture, request)

	if result.Skipped.Referenced != 1 {
		t.Errorf("Skipped.Referenced = %d, want 1: the only citation of %s is a comment on an open wisp",
			result.Skipped.Referenced, cited)
	}
	if result.Swept != 0 {
		t.Errorf("Swept = %d, want 0", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 1, cited)

	// And the same request without the protection deletes it, so the case
	// cannot pass for an implementation that skipped every candidate.
	request.ProtectReferenced = false
	unprotected := sweeperSweep(t, ctx, fixture, request)
	if unprotected.Swept != 1 {
		t.Errorf("unprotected Swept = %d, want 1", unprotected.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, cited)
}

// RunSweeperProtectsCitedRows pins ProtectReferenced
// (issueops.SweepRequest.ProtectReferenced): a candidate whose id appears in a
// not-done bead's description is held back, counted, and named in the bounded
// sample — and the SAME request without the protection deletes it.
//
// The two halves matter together: asserting only the protection would pass for
// an implementation that skipped every candidate, and asserting only the
// unprotected sweep would pass for one that never scanned at all. The citation
// is at a WORD BOUNDARY with a near-miss beside it, so an implementation that
// fell back to a bare substring search protects a row nothing cites.
func RunSweeperProtectsCitedRows(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	cited := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "ref", "cited", false), nil)
	free := sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "ref", "free", false), nil)

	citing := sweeperIssue(fixture, "ref", "live", false)
	citing.Status = types.StatusOpen
	citing.ClosedAt = nil
	// The near-miss `<free>x` must NOT protect anything; the parenthesised
	// occurrence must.
	citing.Description = fmt.Sprintf("superseded by (%s). unrelated: %sx", cited, free)
	// The citing bead is OPEN, so the pattern below must not admit it as a
	// candidate: the closed recheck would skip it either way, but a case relying
	// on that would be asserting the wrong protection.
	sweeperSeedOpen(t, ctx, fixture, citing)

	request := publicops.SweepRequest{
		Tier:              publicops.SweepDurable,
		IDPattern:         sweeperPattern(fixture, "ref"),
		ProtectReferenced: true,
	}
	result := sweeperSweep(t, ctx, fixture, request)

	if result.Skipped.Referenced != 1 {
		t.Fatalf("Skipped.Referenced = %d, want 1 (sample %v)", result.Skipped.Referenced, result.ReferencedIDs)
	}
	if len(result.ReferencedIDs) != 1 || result.ReferencedIDs[0] != cited {
		t.Errorf("ReferencedIDs = %v, want [%s]", result.ReferencedIDs, cited)
	}
	if result.Swept != 1 {
		t.Errorf("Swept = %d, want 1 — the uncited candidate goes", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 1, cited)
	sweeperAssertIssueRows(t, ctx, fixture, 0, free)

	// Without the protection the same candidate goes, and nothing is reported
	// as protected — a caller reading Referenced=0 without having asked has
	// learned nothing.
	request.ProtectReferenced = false
	result = sweeperSweep(t, ctx, fixture, request)
	if result.Skipped.Referenced != 0 {
		t.Errorf("Skipped.Referenced = %d with ProtectReferenced false, want 0", result.Skipped.Referenced)
	}
	if result.Swept != 1 {
		t.Errorf("Swept = %d, want 1 — the cited candidate is no longer protected", result.Swept)
	}
	sweeperAssertIssueRows(t, ctx, fixture, 0, cited)
}

// RunSweeperEmptyMatchIsZeroAndNil pins the not-found story
// (issueops.Sweeper.Sweep, "A REQUEST THAT MATCHES NOTHING"): a zero result and
// a nil error.
func RunSweeperEmptyMatchIsZeroAndNil(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	for _, tier := range []publicops.SweepTier{publicops.SweepDurable, publicops.SweepEphemeral} {
		t.Run(string(tier), func(t *testing.T) {
			result, err := fixture.Sweeper.Sweep(ctx, publicops.SweepRequest{
				Tier:      tier,
				IDPattern: fixture.IssuePrefix + "-none-ever-*",
			})
			if err != nil {
				t.Fatalf("Sweep(nothing matches) error = %v, want nil", err)
			}
			if result.Swept != 0 {
				t.Errorf("Swept = %d, want 0", result.Swept)
			}
			if len(result.ReferencedIDs) != 0 {
				t.Errorf("ReferencedIDs = %v, want empty", result.ReferencedIDs)
			}
		})
	}
}

// RunSweeperRecordsAtMostOneHistoryEntry pins the versioning clause
// (issueops.Sweeper.Sweep, "A DRY RUN CHANGES NOTHING, including history"): a
// dry run and a no-op record NONE, and a sweep that deleted rows records at
// most ONE — the deletion is one act, not one per row.
//
// "AT MOST" rather than "exactly" is the honest promise across these backends:
// the server-backed store records a Dolt commit, the embedded one commits
// outside the SQL transaction and records none here, and an ephemeral sweep
// touches only tables the version-control plane ignores.
func RunSweeperRecordsAtMostOneHistoryEntry(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the entry-per-call clause is unobservable here")
	}
	ids := []string{
		sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "hist", "1", false), nil),
		sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, "hist", "2", false), nil),
	}
	request := publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: sweeperPattern(fixture, "hist"),
	}

	before := sweeperHistory(t, ctx, fixture)
	sweeperSweep(t, ctx, fixture, sweeperWithDryRun(request, true))
	if after := sweeperHistory(t, ctx, fixture); after != before {
		t.Errorf("history went %d -> %d across a DRY RUN, want no entry at all", before, after)
	}

	before = sweeperHistory(t, ctx, fixture)
	sweeperSweep(t, ctx, fixture, publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: fixture.IssuePrefix + "-hist-nothing-*",
	})
	if after := sweeperHistory(t, ctx, fixture); after != before {
		t.Errorf("history went %d -> %d across a sweep that deleted nothing, want no entry", before, after)
	}

	before = sweeperHistory(t, ctx, fixture)
	sweeperSweep(t, ctx, fixture, request)
	after := sweeperHistory(t, ctx, fixture)
	if after < before || after > before+1 {
		t.Errorf("history went %d -> %d across one sweep of %d rows, want at most one more entry",
			before, after, len(ids))
	}
}

// RunSweeperDoesNotMutateTheCallerRequest pins the no-mutation promise
// (issueops.SweepRequest: "Implementations never mutate caller-owned request
// values"). ClosedBefore is the pointer that would otherwise be written
// through, and a normalization step is exactly the kind of edit that does it.
func RunSweeperDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture SweeperFixture) {
	t.Helper()
	cutoff := sweeperCutoff
	request := publicops.SweepRequest{
		Tier:              publicops.SweepDurable,
		Actor:             "sweeper-contract",
		ClosedBefore:      &cutoff,
		IDPattern:         sweeperPattern(fixture, "immutable"),
		ProtectReferenced: true,
		DryRun:            true,
	}
	snapshot := request
	snapshotCutoff := cutoff

	if _, err := fixture.Sweeper.Sweep(ctx, request); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if !reflect.DeepEqual(request, snapshot) {
		t.Errorf("request changed across the call: got %+v, want %+v", request, snapshot)
	}
	if !cutoff.Equal(snapshotCutoff) {
		t.Errorf("the caller's cutoff changed: got %v, want %v", cutoff, snapshotCutoff)
	}
}

// --- fixture helpers -------------------------------------------------------

// sweeperPattern is the glob that scopes one case's ids: a sweep is asked of a
// whole tier, so every case needs one.
func sweeperPattern(fixture SweeperFixture, tag string) string {
	return fixture.IssuePrefix + "-" + tag + "-*"
}

func sweeperIssue(fixture SweeperFixture, tag, name string, ephemeral bool) *types.Issue {
	closedAt := sweeperClosedAt
	return &types.Issue{
		ID:        fmt.Sprintf("%s-%s-%s", fixture.IssuePrefix, tag, name),
		Title:     tag + " " + name,
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
		ClosedAt:  &closedAt,
	}
}

// sweeperSeed writes one issue through the plane its Ephemeral flag names and
// returns its id. mutate runs before the write, which is how a case seeds a
// pinned row or moves a closed_at.
func sweeperSeed(t *testing.T, ctx context.Context, fixture SweeperFixture, issue *types.Issue, mutate func(*types.Issue)) string {
	t.Helper()
	if mutate != nil {
		mutate(issue)
	}
	create := fixture.CreateIssue
	if issue.Ephemeral {
		create = fixture.CreateWisp
	}
	if err := create(ctx, issue, "sweeper-seed"); err != nil {
		t.Fatalf("seeding %s: %v", issue.ID, err)
	}
	return issue.ID
}

func sweeperSeedClosedIssue(t *testing.T, ctx context.Context, fixture SweeperFixture, tag string, ephemeral bool) string {
	t.Helper()
	name := "durable"
	if ephemeral {
		name = "wisp"
	}
	return sweeperSeed(t, ctx, fixture, sweeperIssue(fixture, tag, name, ephemeral), nil)
}

func sweeperSeedOpen(t *testing.T, ctx context.Context, fixture SweeperFixture, issue *types.Issue) string {
	t.Helper()
	issue.Status = types.StatusOpen
	issue.ClosedAt = nil
	return sweeperSeed(t, ctx, fixture, issue, nil)
}

func sweeperWithDryRun(request publicops.SweepRequest, dryRun bool) publicops.SweepRequest {
	request.DryRun = dryRun
	return request
}

func sweeperSweep(t *testing.T, ctx context.Context, fixture SweeperFixture, request publicops.SweepRequest) publicops.SweepResult {
	t.Helper()
	result, err := fixture.Sweeper.Sweep(ctx, request)
	if err != nil {
		t.Fatalf("Sweep(%+v) error = %v", request, err)
	}
	return result
}

// sweeperAssertIssueRows counts the named ids in the ISSUES plane. It is the
// only assertion in this file that does not trust the result the sweep
// reported about itself.
func sweeperAssertIssueRows(t *testing.T, ctx context.Context, fixture SweeperFixture, want int, ids ...string) {
	t.Helper()
	sweeperAssertRows(t, ctx, fixture, "issues", want, ids...)
}

func sweeperAssertWispRows(t *testing.T, ctx context.Context, fixture SweeperFixture, want int, ids ...string) {
	t.Helper()
	sweeperAssertRows(t, ctx, fixture, "wisps", want, ids...)
}

func sweeperAssertRows(t *testing.T, ctx context.Context, fixture SweeperFixture, table string, want int, ids ...string) {
	t.Helper()
	var got int
	args := make([]any, len(ids))
	placeholders := ""
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id IN (%s)", table, placeholders)
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("counting %s rows for %v: %v", table, ids, err)
	}
	if got != want {
		t.Errorf("%s rows for %v = %d, want %d", table, ids, got, want)
	}
}

func sweeperHistory(t *testing.T, ctx context.Context, fixture SweeperFixture) int {
	t.Helper()
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory: %v", err)
	}
	return entries
}
