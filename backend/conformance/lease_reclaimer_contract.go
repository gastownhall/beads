package conformance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.LeaseReclaimer
// must satisfy. Each case asserts what issueops/leasereclaimer.go PROMISES,
// cited by symbol.
//
// FOUR LEGS, ONE BODY. dolt, embedded dolt and the unit-of-work provider all
// reach internal/storage/issueops.ExecuteReclaimInTx, and the http leg reaches
// the same body on the far side of POST /v0/beads/issues:reclaim. So a per-leg
// failure here is a wrapper's failure — a lost scope, a revision that never
// reached the caller, a refusal that stopped matching errors.Is — and the
// cases are written to catch exactly those.
//
// EVERY SWEEP HERE IS SCOPED. The legs share one database per suite with every
// other role's cases, and an UNSCOPED reclaim would revert whatever stale lease
// another case left behind. Every call below names its ids, or an assignee or
// label this case alone uses, so a case can never reach outside what it seeded.
// The workspace-wide form is the same SQL with the scope clause absent, which
// sqlbuild.ReclaimScopeSQL's own tests pin.
//
// STALENESS IS MANUFACTURED, NOT WAITED FOR. The role refuses a negative
// OlderThan (the raw method's -time.Hour trick is exactly what it rules out),
// so a case claims through the leg's real Claimer — which writes the lease row
// every reclaim reads — and then moves that row's lease_expires_at into the
// past through Exec.
type LeaseReclaimerFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// LeaseReclaimer is the surface under test.
	LeaseReclaimer publicops.LeaseReclaimer
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// Claimer takes the claim, and with it the lease row, a reclaim reverts. It
	// is the leg's own role rather than a raw insert so the lease is the shape
	// a real claim leaves, granted_node included.
	Claimer publicops.Claimer
	// Lifecycle performs the guarded write the revision case races against the
	// reclaim. A nil Lifecycle SKIPS that case loudly.
	Lifecycle publicops.Lifecycle
	// Exec runs raw statements against the backing database: the cases use it
	// to age a lease and to attach labels. A nil Exec SKIPS every case that
	// needs a stale lease, which is nearly all of them, so every leg supplies
	// one.
	Exec func(ctx context.Context, statements []SQLStatement) error
	// QueryScalar runs a single-row query and scans it, so the post-state is
	// read RAW rather than through the role being tested.
	QueryScalar func(context.Context, string, []any, ...any) error
	// HookedLeaseReclaimer is the same role taken off the leg's HOOK-FIRING
	// chain (storage.HookFiringStore on the store legs and the http leg,
	// uow.NewNotifyingProvider on the unit-of-work leg), built over the runner
	// FiredUpdates reads. Nil, with FiredUpdates, SKIPS the hook case loudly.
	HookedLeaseReclaimer publicops.LeaseReclaimer
	// FiredUpdates reports, in firing order, the issue id of every on_update
	// the hooked chain ran so far. It must not return before the hooks it is
	// asked about have finished.
	FiredUpdates func(context.Context) ([]string, error)
}

const (
	leaseReclaimerReaper = "reaper"
	leaseReclaimerHolder = "lr-holder"
)

// RunLeaseReclaimerRevertsAStaleLease pins the post-state the role names
// (issueops/leasereclaimer.go, LeaseReclaimer: "each reclaimed row moves
// in_progress -> open and the assignee is cleared") and the three members of
// the ReclaimedLease it reports, each READ RAW.
func RunLeaseReclaimerRevertsAStaleLease(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-revert"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)

	result := leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{id}},
	})
	if len(result.Reclaimed) != 1 {
		t.Fatalf("Reclaimed = %+v, want exactly %s", result.Reclaimed, id)
	}
	got := result.Reclaimed[0]
	if got.ID != id || got.PreviousOwner != leaseReclaimerHolder {
		t.Fatalf("Reclaimed[0] = %+v, want {ID:%s PreviousOwner:%s}", got, id, leaseReclaimerHolder)
	}

	if status := leaseReclaimerScalar[string](t, ctx, fixture, "SELECT status FROM issues WHERE id = ?", id); status != string(types.StatusOpen) {
		t.Fatalf("status after reclaim = %q, want %q", status, types.StatusOpen)
	}
	if held := leaseReclaimerScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM issues WHERE id = ? AND (COALESCE(assignee, '') <> '' OR started_at IS NOT NULL)", id); held != 0 {
		t.Fatal("reclaim left the assignee or started_at set; the row is still held by a dead worker")
	}
	if leases := leaseReclaimerScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); leases != 0 {
		t.Fatalf("lease rows after reclaim = %d, want 0", leases)
	}
	stored := leaseReclaimerScalar[int64](t, ctx, fixture, "SELECT row_lock FROM issues WHERE id = ?", id)
	if got.Revision != types.RevisionToken(stored) {
		t.Fatalf("Reclaimed[0].Revision = %q, want the row's own token %q", got.Revision, types.RevisionToken(stored))
	}
}

// RunLeaseReclaimerMintsARevisionThatFencesTheFormerHolder is the reason the
// revision is minted at all (LeaseReclaimer: "a racing holder's guarded write
// fails with ErrVersionMismatch"). A worker that lost its lease and wakes up
// holding the token its claim handed it must NOT be able to land a guarded
// write over the issue another worker may already have taken; the token the
// reclaim reported, and only it, lands.
func RunLeaseReclaimerMintsARevisionThatFencesTheFormerHolder(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	if fixture.Lifecycle == nil {
		t.Skip("LeaseReclaimerFixture.Lifecycle is nil: this backend cannot race a guarded write against the reclaim")
	}
	id := fixture.IssuePrefix + "-fence"
	claimed := leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)[id]

	result := leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{id}},
	})
	if len(result.Reclaimed) != 1 {
		t.Fatalf("Reclaimed = %+v, want exactly %s", result.Reclaimed, id)
	}
	minted, err := types.ParseRevisionToken(result.Reclaimed[0].Revision)
	if err != nil {
		t.Fatalf("Reclaimed[0].Revision %q is not a revision token: %v", result.Reclaimed[0].Revision, err)
	}
	if minted == claimed {
		t.Fatalf("reclaim reported revision %d, the claim's own token: nothing was minted", minted)
	}

	stale := claimed
	_, err = fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: leaseReclaimerHolder, IssueID: id, ExpectedVersion: &stale,
		Patch: publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "late write"}},
	})
	if !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("guarded write with the pre-reclaim token = %v, want ErrVersionMismatch", err)
	}

	fresh := minted
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: leaseReclaimerReaper, IssueID: id, ExpectedVersion: &fresh,
		Patch: publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "after reclaim"}},
	}); err != nil {
		t.Fatalf("guarded write with the token the reclaim reported = %v, want it to land", err)
	}
}

// RunLeaseReclaimerLeavesALiveLeaseAlone pins staleness as the gate: a lease
// that has not expired is not eligible, even when its id is named, and naming
// it is not an error (ReclaimRequest.Filter: "FILTER.IDS IS NOT AN ERROR WHEN
// IT NAMES A NON-STALE OR UNKNOWN ISSUE").
func RunLeaseReclaimerLeavesALiveLeaseAlone(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-live"
	leaseReclaimerSeedClaimed(t, ctx, fixture, leaseReclaimerHolder, id)

	result := leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{id}},
	})
	if len(result.Reclaimed) != 0 {
		t.Fatalf("Reclaimed = %+v, want nothing: the lease has not expired", result.Reclaimed)
	}
	leaseReclaimerRequireHeld(t, ctx, fixture, id)
}

// RunLeaseReclaimerHonorsTheGraceWindow pins ReclaimRequest.OlderThan: a lease
// expired less than OlderThan ago is left alone, and the same lease is
// reclaimed once the window is narrower than its age.
func RunLeaseReclaimerHonorsTheGraceWindow(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-grace"
	leaseReclaimerSeedClaimed(t, ctx, fixture, leaseReclaimerHolder, id)
	leaseReclaimerAge(t, ctx, fixture, 10*time.Minute, id)

	request := publicops.ReclaimRequest{
		Actor:     leaseReclaimerReaper,
		OlderThan: time.Hour,
		Filter:    publicops.ReclaimFilter{IDs: []string{id}},
	}
	if result := leaseReclaim(t, ctx, fixture, request); len(result.Reclaimed) != 0 {
		t.Fatalf("OlderThan 1h over a lease 10m stale: Reclaimed = %+v, want nothing", result.Reclaimed)
	}
	leaseReclaimerRequireHeld(t, ctx, fixture, id)

	request.OlderThan = time.Minute
	if got := leaseReclaimerIDs(leaseReclaim(t, ctx, fixture, request)); !slices.Equal(got, []string{id}) {
		t.Fatalf("OlderThan 1m over a lease 10m stale: Reclaimed = %v, want [%s]", got, id)
	}
}

// RunLeaseReclaimerScopesToTheNamedIDs pins the IDs scope from both sides: an
// id that is not named is never reverted though it is stale, and an id that is
// named but absent or live is simply missing from Reclaimed — the call does
// not fail and does not report it.
func RunLeaseReclaimerScopesToTheNamedIDs(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	named := fixture.IssuePrefix + "-scope-named"
	unnamed := fixture.IssuePrefix + "-scope-unnamed"
	live := fixture.IssuePrefix + "-scope-live"
	ghost := fixture.IssuePrefix + "-scope-ghost"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, named, unnamed)
	leaseReclaimerSeedClaimed(t, ctx, fixture, leaseReclaimerHolder, live)

	result := leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{ghost, live, named}},
	})
	if got := leaseReclaimerIDs(result); !slices.Equal(got, []string{named}) {
		t.Fatalf("Reclaimed = %v, want [%s]: only the named stale lease", got, named)
	}
	leaseReclaimerRequireHeld(t, ctx, fixture, unnamed)
	leaseReclaimerRequireHeld(t, ctx, fixture, live)
}

// RunLeaseReclaimerAnswersAnAbsentIDWithAnEmptyResult pins the empty answer:
// a sweep scoped only to ids that name no row is not a refusal, and its
// Reclaimed is empty and NON-NIL (ReclaimResult.Reclaimed: "Never nil for a
// successful call").
func RunLeaseReclaimerAnswersAnAbsentIDWithAnEmptyResult(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	result, err := fixture.LeaseReclaimer.Reclaim(ctx, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{fixture.IssuePrefix + "-absent-a", fixture.IssuePrefix + "-absent-b"}},
	})
	if err != nil {
		t.Fatalf("Reclaim scoped to absent ids = %v, want an empty answer", err)
	}
	if result.Reclaimed == nil {
		t.Fatal("Reclaimed is nil; the role promises an empty, non-nil slice")
	}
	if len(result.Reclaimed) != 0 {
		t.Fatalf("Reclaimed = %+v, want nothing", result.Reclaimed)
	}
}

// RunLeaseReclaimerScopesToAssigneesAndLabels re-homes the raw method's scope
// cases (testReclaimScoped) onto the role: assignee, label (AND), label-any
// (OR) and exclude-label each narrow the sweep and never widen it.
func RunLeaseReclaimerScopesToAssigneesAndLabels(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	p := fixture.IssuePrefix
	a1, a2, b1, c1 := p+"-lbl-a1", p+"-lbl-a2", p+"-lbl-b1", p+"-lbl-c1"
	laneA, laneB, opus := p+"-lane-a", p+"-lane-b", p+"-tier-opus"
	workerA, workerB, workerC := p+"-wa", p+"-wb", p+"-wc"
	leaseReclaimerSeedStale(t, ctx, fixture, workerA, a1, a2)
	leaseReclaimerSeedStale(t, ctx, fixture, workerB, b1)
	leaseReclaimerSeedStale(t, ctx, fixture, workerC, c1)
	leaseReclaimerLabel(t, ctx, fixture, a1, laneA)
	leaseReclaimerLabel(t, ctx, fixture, a2, laneA)
	leaseReclaimerLabel(t, ctx, fixture, a2, opus)
	leaseReclaimerLabel(t, ctx, fixture, b1, laneB)

	reclaim := func(filter publicops.ReclaimFilter) []string {
		t.Helper()
		return leaseReclaimerIDs(leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{Actor: leaseReclaimerReaper, Filter: filter}))
	}

	if got := reclaim(publicops.ReclaimFilter{Labels: []string{laneB}}); !slices.Equal(got, []string{b1}) {
		t.Fatalf("reclaim(labels %s) = %v, want [%s]", laneB, got, b1)
	}
	if got := reclaim(publicops.ReclaimFilter{Assignees: []string{workerC}}); !slices.Equal(got, []string{c1}) {
		t.Fatalf("reclaim(assignees %s) = %v, want [%s]", workerC, got, c1)
	}
	if got := reclaim(publicops.ReclaimFilter{Labels: []string{laneA}, ExcludeLabels: []string{opus}}); !slices.Equal(got, []string{a1}) {
		t.Fatalf("reclaim(labels %s, exclude %s) = %v, want [%s]", laneA, opus, got, a1)
	}
	leaseReclaimerRequireHeld(t, ctx, fixture, a2)
	if got := reclaim(publicops.ReclaimFilter{LabelsAny: []string{p + "-lane-z", opus}}); !slices.Equal(got, []string{a2}) {
		t.Fatalf("reclaim(labels-any lane-z,%s) = %v, want [%s]", opus, got, a2)
	}
}

// RunLeaseReclaimerAttributesTheRecoveryEvent pins ReclaimRequest.Actor: the
// recovery event each reverted row records names the actor who ran the sweep,
// with the lease's former holder beside it.
func RunLeaseReclaimerAttributesTheRecoveryEvent(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-event"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)
	leaseReclaim(t, ctx, fixture, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{id}},
	})
	if n := leaseReclaimerScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND actor = ? AND old_value = ?",
		id, string(types.EventLeaseReclaimed), leaseReclaimerReaper, leaseReclaimerHolder); n != 1 {
		t.Fatalf("lease_reclaimed events by %s naming %s = %d, want 1", leaseReclaimerReaper, leaseReclaimerHolder, n)
	}
}

// RunLeaseReclaimerFiresOnUpdateOncePerReclaimedRow pins the hook the role
// names (LeaseReclaimer: "ITS HOOK IS on_update, once per reverted row"): each
// reverted row fires on_update exactly once, and a row the sweep did not
// revert — named but live — fires nothing.
func RunLeaseReclaimerFiresOnUpdateOncePerReclaimedRow(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	if fixture.HookedLeaseReclaimer == nil || fixture.FiredUpdates == nil {
		t.Skip("LeaseReclaimerFixture.HookedLeaseReclaimer/FiredUpdates is nil: this backend cannot observe its hooks")
	}
	first := fixture.IssuePrefix + "-hook-a"
	second := fixture.IssuePrefix + "-hook-b"
	live := fixture.IssuePrefix + "-hook-live"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, first, second)
	leaseReclaimerSeedClaimed(t, ctx, fixture, leaseReclaimerHolder, live)
	before, err := fixture.FiredUpdates(ctx)
	if err != nil {
		t.Fatalf("FiredUpdates before: %v", err)
	}

	result, err := fixture.HookedLeaseReclaimer.Reclaim(ctx, publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: []string{first, second, live}},
	})
	if err != nil {
		t.Fatalf("hooked Reclaim: %v", err)
	}
	if got := leaseReclaimerIDs(result); !slices.Equal(got, []string{first, second}) {
		t.Fatalf("Reclaimed = %v, want [%s %s]", got, first, second)
	}

	after, err := fixture.FiredUpdates(ctx)
	if err != nil {
		t.Fatalf("FiredUpdates after: %v", err)
	}
	fired := map[string]int{}
	for _, id := range after[len(before):] {
		fired[id]++
	}
	for _, id := range []string{first, second} {
		if fired[id] != 1 {
			t.Fatalf("on_update fired %d time(s) for %s, want exactly 1 (fired: %v)", fired[id], id, after[len(before):])
		}
	}
	if fired[live] != 0 {
		t.Fatalf("on_update fired for %s, which the sweep did not revert", live)
	}
}

// RunLeaseReclaimerRefusesAMalformedRequest pins every refusal the role lists,
// each as ErrValidation (the cap as *TooManyReclaimIDsError), and that a
// refusal changes nothing: the stale lease every request names survives all of
// them.
func RunLeaseReclaimerRefusesAMalformedRequest(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-malformed"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)

	tooMany := make([]string, publicops.MaxReclaimIDs+1)
	for i := range tooMany {
		tooMany[i] = id
	}
	for _, test := range []struct {
		name    string
		request publicops.ReclaimRequest
		tooMany bool
	}{
		{"an empty actor", publicops.ReclaimRequest{Filter: publicops.ReclaimFilter{IDs: []string{id}}}, false},
		// One past the column every reverted row's recovery event records it
		// in: a typed refusal before the sweep, never the backend's own error
		// from the first row it would have reverted.
		{"an over-long actor", publicops.ReclaimRequest{Actor: strings.Repeat("r", types.MaxFieldLen+1),
			Filter: publicops.ReclaimFilter{IDs: []string{id}}}, false},
		{"a negative grace window", publicops.ReclaimRequest{Actor: leaseReclaimerReaper, OlderThan: -time.Second,
			Filter: publicops.ReclaimFilter{IDs: []string{id}}}, false},
		{"a blank scoped id", publicops.ReclaimRequest{Actor: leaseReclaimerReaper,
			Filter: publicops.ReclaimFilter{IDs: []string{id, ""}}}, false},
		{"more ids than the cap", publicops.ReclaimRequest{Actor: leaseReclaimerReaper,
			Filter: publicops.ReclaimFilter{IDs: tooMany}}, true},
	} {
		_, err := fixture.LeaseReclaimer.Reclaim(ctx, test.request)
		if !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("%s: Reclaim error = %v, want ErrValidation", test.name, err)
		}
		if test.tooMany {
			var capErr *publicops.TooManyReclaimIDsError
			if !errors.As(err, &capErr) || capErr.Requested != len(tooMany) || capErr.Cap != publicops.MaxReclaimIDs {
				t.Fatalf("%s: Reclaim error = %v, want *TooManyReclaimIDsError{Requested:%d Cap:%d}",
					test.name, err, len(tooMany), publicops.MaxReclaimIDs)
			}
		}
		leaseReclaimerRequireHeld(t, ctx, fixture, id)
	}
}

// RunLeaseReclaimerRefusesABlankScopeEntry pins the blank-entry refusal on
// the scopes that are not ids: an empty or all-blank assignee or label is
// ErrValidation as a *ReclaimFieldError naming its field, and changes
// nothing — the stale lease every request names survives.
func RunLeaseReclaimerRefusesABlankScopeEntry(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-blankscope"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)

	for _, test := range []struct {
		field  string
		filter publicops.ReclaimFilter
	}{
		{publicops.ReclaimFieldAssignees, publicops.ReclaimFilter{IDs: []string{id}, Assignees: []string{leaseReclaimerHolder, " "}}},
		{publicops.ReclaimFieldLabels, publicops.ReclaimFilter{IDs: []string{id}, Labels: []string{""}}},
		{publicops.ReclaimFieldLabelsAny, publicops.ReclaimFilter{IDs: []string{id}, LabelsAny: []string{"\t"}}},
		{publicops.ReclaimFieldExcludeLabels, publicops.ReclaimFilter{IDs: []string{id}, ExcludeLabels: []string{"x", "  "}}},
	} {
		_, err := fixture.LeaseReclaimer.Reclaim(ctx, publicops.ReclaimRequest{Actor: leaseReclaimerReaper, Filter: test.filter})
		var fieldErr *publicops.ReclaimFieldError
		if !errors.Is(err, publicops.ErrValidation) || !errors.As(err, &fieldErr) || fieldErr.Field != test.field {
			t.Fatalf("blank %s entry: Reclaim error = %v, want a *ReclaimFieldError naming %s", test.field, err, test.field)
		}
		leaseReclaimerRequireHeld(t, ctx, fixture, id)
	}
}

// RunLeaseReclaimerAcceptsExactlyTheCap pins the cap's boundary: MaxReclaimIDs
// entries is a request, one more is a refusal (the case above).
func RunLeaseReclaimerAcceptsExactlyTheCap(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	ids := make([]string, publicops.MaxReclaimIDs)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-cap-%04d", fixture.IssuePrefix, i)
	}
	result, err := fixture.LeaseReclaimer.Reclaim(ctx, publicops.ReclaimRequest{
		Actor: leaseReclaimerReaper, Filter: publicops.ReclaimFilter{IDs: ids},
	})
	if err != nil {
		t.Fatalf("Reclaim with exactly MaxReclaimIDs ids = %v, want an answer", err)
	}
	if len(result.Reclaimed) != 0 {
		t.Fatalf("Reclaimed = %+v, want nothing: none of the ids exist", result.Reclaimed)
	}
}

// RunLeaseReclaimerDoesNotMutateTheCallerRequest pins the role's "never mutate
// caller-owned request values": the scope slices come back as they were sent.
func RunLeaseReclaimerDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture) {
	t.Helper()
	requireLeaseReclaimerExec(t, fixture)
	id := fixture.IssuePrefix + "-nomut"
	leaseReclaimerSeedStale(t, ctx, fixture, leaseReclaimerHolder, id)
	ids := []string{fixture.IssuePrefix + "-nomut-ghost", id}
	assignees := []string{leaseReclaimerHolder}
	request := publicops.ReclaimRequest{
		Actor:  leaseReclaimerReaper,
		Filter: publicops.ReclaimFilter{IDs: ids, Assignees: assignees},
	}
	leaseReclaim(t, ctx, fixture, request)
	if !slices.Equal(ids, []string{fixture.IssuePrefix + "-nomut-ghost", id}) || !slices.Equal(assignees, []string{leaseReclaimerHolder}) {
		t.Fatalf("Reclaim rewrote the caller's scope: ids=%v assignees=%v", ids, assignees)
	}
}

// NewUpdateHookLog plants an on_update script that appends the id of every
// issue it fires for, and returns the runner a hook-firing chain is built over
// plus a reader for LeaseReclaimerFixture.FiredUpdates. The reader waits for
// in-flight hooks before reading, so it answers for every hook already fired.
//
// It answers (nil, nil) where a shell hook cannot run, so a fixture built from
// it leaves the hook case to skip rather than fail.
func NewUpdateHookLog(t *testing.T) (*hooks.Runner, func(context.Context) ([]string, error)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "on_update.log")
	script := "#!/bin/sh\necho \"$1\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "on_update"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant the on_update hook: %v", err)
	}
	runner := hooks.NewRunner(dir)
	return runner, func(context.Context) ([]string, error) {
		if !runner.Wait(30 * time.Second) {
			return nil, errors.New("on_update hooks still running after 30s")
		}
		data, err := os.ReadFile(log) // #nosec G304 -- this helper's own temp file
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				ids = append(ids, line)
			}
		}
		return ids, nil
	}
}

func requireLeaseReclaimerExec(t *testing.T, fixture LeaseReclaimerFixture) {
	t.Helper()
	if fixture.Exec == nil || fixture.Claimer == nil || fixture.QueryScalar == nil {
		t.Skip("LeaseReclaimerFixture.Exec/Claimer/QueryScalar is nil: this backend cannot manufacture a stale lease")
	}
}

// leaseReclaimerSeedClaimed creates each id and claims it as holder through the
// leg's Claimer, answering each claim's row version.
func leaseReclaimerSeedClaimed(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, holder string, ids ...string) map[string]int64 {
	t.Helper()
	versions := map[string]int64{}
	for _, id := range ids {
		if err := fixture.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		claimed, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: holder, IssueID: id})
		if err != nil {
			t.Fatalf("claim %s as %s: %v", id, holder, err)
		}
		if claimed.Issue == nil {
			t.Fatalf("claim %s answered no issue", id)
		}
		versions[id] = claimed.Issue.RowVersion
	}
	return versions
}

// leaseReclaimerSeedStale claims each id and ages its lease an hour past
// expiry, far beyond any grace window a case below uses by default.
func leaseReclaimerSeedStale(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, holder string, ids ...string) map[string]int64 {
	t.Helper()
	versions := leaseReclaimerSeedClaimed(t, ctx, fixture, holder, ids...)
	leaseReclaimerAge(t, ctx, fixture, time.Hour, ids...)
	return versions
}

// leaseReclaimerAge moves each id's lease expiry to age in the past.
func leaseReclaimerAge(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, age time.Duration, ids ...string) {
	t.Helper()
	expired := time.Now().UTC().Add(-age)
	statements := make([]SQLStatement, 0, len(ids))
	for _, id := range ids {
		statements = append(statements, SQLStatement{
			Query: "UPDATE leases SET lease_expires_at = ? WHERE issue_id = ?",
			Args:  []any{expired, id},
		})
	}
	if err := fixture.Exec(ctx, statements); err != nil {
		t.Fatalf("age leases %v: %v", ids, err)
	}
	for _, id := range ids {
		if n := leaseReclaimerScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 1 {
			t.Fatalf("lease rows for %s = %d, want 1: the claim wrote no lease to reclaim", id, n)
		}
	}
}

func leaseReclaimerLabel(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, id, label string) {
	t.Helper()
	if err := fixture.Exec(ctx, []SQLStatement{{
		Query: "INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		Args:  []any{id, label},
	}}); err != nil {
		t.Fatalf("label %s %s: %v", id, label, err)
	}
}

func leaseReclaimerRequireHeld(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, id string) {
	t.Helper()
	if status := leaseReclaimerScalar[string](t, ctx, fixture, "SELECT status FROM issues WHERE id = ?", id); status != string(types.StatusInProgress) {
		t.Fatalf("%s status = %q, want it still %q", id, status, types.StatusInProgress)
	}
}

func leaseReclaim(t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, request publicops.ReclaimRequest) publicops.ReclaimResult {
	t.Helper()
	result, err := fixture.LeaseReclaimer.Reclaim(ctx, request)
	if err != nil {
		t.Fatalf("Reclaim(%+v): %v", request.Filter, err)
	}
	if result.Reclaimed == nil {
		t.Fatal("Reclaimed is nil; the role promises a non-nil slice for a successful call")
	}
	return result
}

func leaseReclaimerIDs(result publicops.ReclaimResult) []string {
	ids := make([]string, 0, len(result.Reclaimed))
	for _, r := range result.Reclaimed {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

func leaseReclaimerScalar[T any](t *testing.T, ctx context.Context, fixture LeaseReclaimerFixture, query string, args ...any) T {
	t.Helper()
	var value T
	if err := fixture.QueryScalar(ctx, query, args, &value); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return value
}
