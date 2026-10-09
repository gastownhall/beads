//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_releaser_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Releaser contract, run through the http client against a real bd serve.
//
// TWO OF THE ELEVEN ARE PARKED, and both parks are about the wire's refusal
// VOCABULARY rather than about a member it cannot carry — the same shape the two
// create parks have, and they retire the same way. The request itself maps
// member for member: actor, the compare-and-set on the holder, force and the
// path, with nothing left over and no W- row.
//
//   - L-release-notclaimed. The role splits an unheld row (ErrNotClaimed) from a
//     status the transition is not defined over (ErrNotReleasable); the wire
//     spells both `not_releasable` under one code with no member telling them
//     apart, so this client answers the wider of the two.
//   - L-release-notowner. The role's ownership fence is ErrNotOwner; the wire
//     answers `already_claimed`, which is what updateIssue already calls the same
//     situation, so this client answers ErrAlreadyClaimed.
//
// BOTH PARKS HAVE A RUNNING PIN below asserting the degraded-but-real behavior —
// the refusal is typed, it is the right refusal for the situation, and NOTHING IS
// WRITTEN — so the ledger rows retire loudly rather than sitting behind a skip.
// The rest of the contract crosses whole, including the two halves that are
// easiest to get backwards: a matching expectation RELEASES a claim the caller
// does not hold (a match replaces the fence), and the comparison is
// separator-insensitive and nothing else.

// releaseParkBead is the bead the release-side parks cite. It is separate from
// the create and write parks because it retires on a different event: a problem
// code, or a member, that separates two refusals the server currently merges.
const releaseParkBead = "ga-f352s"

func newServedReleaserFixture(t *testing.T, prefix string) conformance.ReleaserFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	releaser, err := env.subject.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	return conformance.ReleaserFixture{
		IssuePrefix:   env.prefix,
		Releaser:      releaser,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		CommitPending: env.commitPending,
	}
}

func TestServedReleaserReleasesItsOwnClaim(t *testing.T) {
	conformance.RunReleaserReleasesItsOwnClaim(t, t.Context(), newServedReleaserFixture(t, "hrl1"))
}

// TestServedReleaserRefusesAForeignClaimUntilForced is PARKED on
// L-release-notowner: the case binds issueops.ErrNotOwner, and the wire answers
// the ownership fence with `already_claimed` — the code updateIssue gives the
// same situation — which this client maps to ErrAlreadyClaimed.
//
// BOTH HALVES of what the case proves are asserted by
// TestServedReleaseAnswersAForeignHolderAsAlreadyClaimed below, which RUNS: the
// stranger is refused with the row untouched, and the forced release then lands.
func TestServedReleaserRefusesAForeignClaimUntilForced(t *testing.T) {
	skipKnownDivergence(t, "L-release-notowner", releaseParkBead,
		"the case binds issueops.ErrNotOwner; the wire answers the release's ownership fence with `already_claimed`, "+
			"which every other operation on this surface means by ErrAlreadyClaimed (asserted, force half included, by "+
			"TestServedReleaseAnswersAForeignHolderAsAlreadyClaimed)")
	conformance.RunReleaserRefusesAForeignClaimUntilForced(t, t.Context(), newServedReleaserFixture(t, "hrl2"))
}

func TestServedReleaserReleasesOnlyTheExpectedHolder(t *testing.T) {
	conformance.RunReleaserReleasesOnlyTheExpectedHolder(t, t.Context(), newServedReleaserFixture(t, "hrl3"))
}

// TestServedReleaserRefusesAnUnheldIssue is PARKED on L-release-notclaimed: two
// of its three arms bind issueops.ErrNotClaimed, and the wire spells that and
// ErrNotReleasable with one code carrying nothing that tells them apart.
//
// The third arm — a conditional release of an unheld row answers
// ErrAssigneeMismatch — is NOT degraded and is asserted by
// TestServedReleaseAnswersTheUnheldRowAsNotReleasable below, which RUNS, beside
// the two degraded ones.
func TestServedReleaserRefusesAnUnheldIssue(t *testing.T) {
	skipKnownDivergence(t, "L-release-notclaimed", releaseParkBead,
		"the unconditional and forced arms bind issueops.ErrNotClaimed; the wire spells an unheld row and an "+
			"unreleasable status under one `not_releasable` code, so this client answers the wider ErrNotReleasable "+
			"(asserted, conditional arm included, by TestServedReleaseAnswersTheUnheldRowAsNotReleasable)")
	conformance.RunReleaserRefusesAnUnheldIssue(t, t.Context(), newServedReleaserFixture(t, "hrl4"))
}

func TestServedReleaserRefusesAStatusThatCannotBeReleased(t *testing.T) {
	conformance.RunReleaserRefusesAStatusThatCannotBeReleased(t, t.Context(), newServedReleaserFixture(t, "hrl5"))
}

func TestServedReleaserRefusesAMalformedRequest(t *testing.T) {
	conformance.RunReleaserRefusesAMalformedRequest(t, t.Context(), newServedReleaserFixture(t, "hrl6"))
}

func TestServedReleaserRefusesAnAbsentID(t *testing.T) {
	conformance.RunReleaserRefusesAnAbsentID(t, t.Context(), newServedReleaserFixture(t, "hrl7"))
}

func TestServedReleaserAttributesTheReleaseToTheActor(t *testing.T) {
	conformance.RunReleaserAttributesTheReleaseToTheActor(t, t.Context(), newServedReleaserFixture(t, "hrl8"))
}

func TestServedReleaserRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunReleaserRecordsExactlyOneHistoryEntry(t, t.Context(), newServedReleaserFixture(t, "hrl9"))
}

// TestServedReleaserReleasesAWispClaimWithoutVersioning is the case that can
// tell the storage legs apart, and it runs here for a reason worth naming: an
// ephemeral release writes a row and changes no versioned table, so a server
// that read the empty table set as "nothing happened" would roll the write back
// and answer this client a wisp that is still claimed.
func TestServedReleaserReleasesAWispClaimWithoutVersioning(t *testing.T) {
	conformance.RunReleaserReleasesAWispClaimWithoutVersioning(t, t.Context(), newServedReleaserFixture(t, "hrl10"))
}

func TestServedReleaserDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunReleaserDoesNotMutateTheCallerRequest(t, t.Context(), newServedReleaserFixture(t, "hrl11"))
}

// TestServedReleaseAnswersTheUnheldRowAsNotReleasable is L-release-notclaimed's
// pin, and it RUNS.
//
// THREE CLAUSES, and only the first is the divergence. The unheld row is refused
// with ErrNotReleasable rather than ErrNotClaimed — the wider of the two the
// server merges — which is what the ledger row is about. The other two are the
// promises that must NOT have moved with it: nothing is written, so the row and
// its version are exactly as they were, and the CONDITIONAL path still answers
// ErrAssigneeMismatch, because that refusal has a code of its own and never fell
// into the merge.
//
// The third clause is the one that makes this pin worth more than the park it
// replaces: a client that had reported every release refusal as one sentinel
// would pass a two-clause version of this test and fail here.
func TestServedReleaseAnswersTheUnheldRowAsNotReleasable(t *testing.T) {
	env := newServedEnv(t, "hrlp1")
	releaser, err := env.subject.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	ctx := t.Context()

	id := env.prefix + "-unheld"
	if err := env.createIssue(ctx, &types.Issue{
		ID: id, Title: "unheld", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	_, _, version := servedRow(t, ctx, env, id)

	for _, tc := range []struct {
		name    string
		request issueops.ReleaseRequest
		want    error
	}{
		{"unconditional", issueops.ReleaseRequest{Actor: "holder", IssueID: id}, issueops.ErrNotReleasable},
		{"forced", issueops.ReleaseRequest{Actor: "reaper", IssueID: id, Force: true}, issueops.ErrNotReleasable},
		{"conditional", issueops.ReleaseRequest{
			Actor: "supervisor", IssueID: id, ExpectedAssignee: strptr("holder"),
		}, issueops.ErrAssigneeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := releaser.Release(ctx, tc.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Release(%s) error = %v, want %v", tc.name, err, tc.want)
			}
			// The role's OTHER sentinel must not arrive: the merge is one
			// direction, and a client that answered both would be inventing the
			// distinction the wire refused to make.
			if errors.Is(err, issueops.ErrNotClaimed) {
				t.Errorf("Release(%s) answered ErrNotClaimed, which this wire cannot carry: "+
					"the code covers an unheld row and an unreleasable status alike", tc.name)
			}
			assertServedRow(t, ctx, env, id, "", types.StatusOpen, version)
		})
	}
}

// TestServedReleaseAnswersAForeignHolderAsAlreadyClaimed is L-release-notowner's
// pin, and it RUNS.
//
// The refusal is ErrAlreadyClaimed rather than ErrNotOwner — the divergence —
// and it arrives as a TYPED *issueops.ClaimConflictError naming the row, which
// is the part that survives whole: the wire leaves the id out because the
// request already said it, and the problem mapper puts it back.
//
// THE FORCED HALF IS WHAT MAKES THE REFUSAL FALSIFIABLE. A client that refused
// every foreign release would pass a refusal-only pin perfectly, and
// refusal-only coverage of a guarded write is half a test. The
// name-the-holder bypass is asserted too, on its own row, because the role
// offers TWO ways past the fence and a pin that checked one would let the other
// rot.
func TestServedReleaseAnswersAForeignHolderAsAlreadyClaimed(t *testing.T) {
	env := newServedEnv(t, "hrlp2")
	releaser, err := env.subject.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	ctx := t.Context()

	const holder = "hrlp2-holder"
	seed := func(name string) string {
		t.Helper()
		id := env.prefix + "-" + name
		if err := env.createIssue(ctx, &types.Issue{
			ID: id, Title: name, Status: types.StatusInProgress, Assignee: holder,
			Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return id
	}

	forced := seed("forced")
	_, _, forcedVersion := servedRow(t, ctx, env, forced)
	_, err = releaser.Release(ctx, issueops.ReleaseRequest{Actor: "stranger", IssueID: forced})
	if !errors.Is(err, issueops.ErrAlreadyClaimed) {
		t.Fatalf("Release() by a stranger error = %v, want ErrAlreadyClaimed", err)
	}
	if errors.Is(err, issueops.ErrNotOwner) {
		t.Errorf("Release() answered ErrNotOwner, which this wire cannot carry: the fence is spelled `already_claimed`")
	}
	var conflict *issueops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Release() error = %v, want a typed *issueops.ClaimConflictError", err)
	}
	if conflict.IssueID != forced {
		t.Errorf("the conflict names %q, want %q: the wire omits the id because the request said it, and the client puts it back",
			conflict.IssueID, forced)
	}
	assertServedRow(t, ctx, env, forced, holder, types.StatusInProgress, forcedVersion)

	// Force bypasses the fence and nothing else.
	result, err := releaser.Release(ctx, issueops.ReleaseRequest{Actor: "reaper", IssueID: forced, Force: true})
	if err != nil {
		t.Fatalf("forced Release() error = %v", err)
	}
	if !result.Changed {
		t.Error("Changed = false on a forced release that wrote the row")
	}
	assertServedRow(t, ctx, env, forced, "", types.StatusOpen, -1)
	if _, _, after := servedRow(t, ctx, env, forced); after == forcedVersion {
		t.Errorf("the forced release left row_version at %d; a release REMINTS the token, which is what makes a "+
			"concurrent reclaim or close conflict rather than silently merge", after)
	}

	// The OTHER bypass: naming the holder replaces the fence, so an actor who is
	// not the holder releases it without force.
	named := seed("named")
	if _, err := releaser.Release(ctx, issueops.ReleaseRequest{
		Actor: "supervisor", IssueID: named, ExpectedAssignee: strptr(holder),
	}); err != nil {
		t.Fatalf("Release() naming the holder error = %v, want the release to land", err)
	}
	assertServedRow(t, ctx, env, named, "", types.StatusOpen, -1)
}

// servedRow reads one issue row RAW: its assignee, its status and its version.
//
// It reads through the reference store's own SQL handle rather than through the
// client, which is the whole point: "the refusal wrote nothing" is the one
// clause a role-answer assertion cannot check, because reading state back
// through the thing under test is exactly the check that passes on a corrupted
// table.
//
// THE VERSION IS THE `row_lock` COLUMN, which is what the contract's own
// releaserRowVersion reads and is COALESCEd for the same reason: a row that
// never took a guarded write carries NULL there, and a nil scan is a fixture
// failure rather than the answer the case wants.
//
// IT IS PART OF THE READ because it is part of the claim. Both pins
// below say a refused release leaves the row AND ITS VERSION untouched, and a
// row whose version moved is not untouched even when every column a reader looks
// at is unchanged: the holder's next compare-and-set would lose, for a reason
// nothing on the row explains. An assertion that stopped at assignee and status
// would have let exactly that through.
func servedRow(t *testing.T, ctx context.Context, env *servedEnv, id string) (assignee, status string, version int64) {
	t.Helper()
	if err := env.queryScalar(ctx,
		"SELECT COALESCE(assignee, ''), status, COALESCE(row_lock, 0) FROM issues WHERE id = ?", []any{id},
		&assignee, &status, &version); err != nil {
		t.Fatalf("read %s back raw: %v", id, err)
	}
	return assignee, status, version
}

// assertServedRow holds a row to an expected assignee and status, and — when
// wantVersion is non-negative — to a version it must still be carrying.
func assertServedRow(t *testing.T, ctx context.Context, env *servedEnv, id, assignee string, status types.Status, wantVersion int64) {
	t.Helper()
	gotAssignee, gotStatus, gotVersion := servedRow(t, ctx, env, id)
	if gotAssignee != assignee || gotStatus != string(status) {
		t.Errorf("row %s = assignee %q status %q, want %q / %q", id, gotAssignee, gotStatus, assignee, status)
	}
	if wantVersion >= 0 && gotVersion != wantVersion {
		t.Errorf("row %s moved to version %d, want %d: a refused release leaves the row AND ITS VERSION untouched, "+
			"and a version that moved makes the holder's next compare-and-set lose for a reason nothing on the row explains",
			id, gotVersion, wantVersion)
	}
}

func strptr(s string) *string { return &s }
