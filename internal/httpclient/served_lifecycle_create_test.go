//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_lifecycle_create_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Lifecycle CREATE contracts, run through client → in-process bd serve →
// reference store.
//
// createIssue is the operation that made D8 row 16 whole: the row was PARTIAL
// for as long as POST /v0/beads/issues was free on the wire, and the three verbs
// beside it — update, close, reopen — have been served since the write-lifecycle
// wave. What is asserted here is the fourth.
//
// TWO OF THE SIX CONTRACTS ARE PARKED on the wire's refusal VOCABULARY rather
// than on a member it cannot carry: the operation answers a dangling edge
// target and a foreign id prefix with the same `400 invalid_argument` every
// other body refusal earns, so the typed sentinels the contracts bind cannot be
// reconstructed. Both parks have a RUNNING pin beside them asserting the
// degraded-but-real behavior, so the ledger rows retire loudly rather than
// sitting behind a skip. The other two parks are a member list — the creation
// stamp and the classification plumbing — and they are refuse-not-drop working
// as designed; the sub-second echo is the second of them because its precision
// pin IS the creation stamp.

func newServedCreateFixture(t *testing.T, prefix string) conformance.LifecycleCreateFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	return conformance.LifecycleCreateFixture{
		IssuePrefix: env.prefix,
		Lifecycle:   lifecycle,
		CreateIssue: env.createIssue,
		GetIssue:    env.getIssue,
		WispExists:  env.wispExists,
	}
}

// TestServedLifecycleCreateRefusesAnOccupiedID is the case the create-only guard
// exists for, and the one that catches the shipped bug the contract was written
// against: `bd create --id <occupied>` silently UPSERTING the stored row and
// reporting success.
func TestServedLifecycleCreateRefusesAnOccupiedID(t *testing.T) {
	conformance.RunLifecycleCreateRefusesAnOccupiedID(t, t.Context(), newServedCreateFixture(t, "hlc1"))
}

func TestServedLifecycleCreateInheritsParentLabels(t *testing.T) {
	conformance.RunLifecycleCreateInheritsParentLabels(t, t.Context(), newServedCreateFixture(t, "hlc2"))
}

// TestServedLifecycleCreateRejectsMissingDependencyTargets is PARKED on
// L-create-notfound: the case binds ErrValidation WRAPPING ErrNotFound and
// requires the refusal to NAME the missing target, and the wire's 400 for a
// dangling edge carries a fixed detail that quotes neither.
//
// The outcome half — the whole request fails and nothing is created — is
// asserted by TestServedCreateRefusesAnAbsentTargetAsValidation below, which
// RUNS.
func TestServedLifecycleCreateRejectsMissingDependencyTargets(t *testing.T) {
	skipKnownDivergence(t, "L-create-notfound", createParkBead,
		"the case binds ErrValidation wrapping ErrNotFound and requires the missing target's id in the message; the wire's 400 for a dangling dependency, parent or waits-for target carries a FIXED detail that reflects neither (asserted by TestServedCreateRefusesAnAbsentTargetAsValidation)")
	conformance.RunLifecycleCreateRejectsMissingDependencyTargets(t, t.Context(), newServedCreateFixture(t, "hlc3"))
}

// TestServedLifecycleCreateRefusesAForeignIDPrefix is PARKED on L-create-prefix:
// the case binds storage.ErrPrefixMismatch, and the wire spells that refusal as
// the same `invalid_argument` / `param: "id"` pair a malformed id earns, so
// nothing on the wire tells the two apart.
func TestServedLifecycleCreateRefusesAForeignIDPrefix(t *testing.T) {
	skipKnownDivergence(t, "L-create-prefix", createParkBead,
		"the case binds storage.ErrPrefixMismatch, and the wire answers a foreign prefix with the same invalid_argument/param=id pair a malformed id earns (asserted by TestServedCreateRefusesAForeignPrefixAsValidation)")
	conformance.RunLifecycleCreateRefusesAForeignIDPrefix(t, t.Context(), newServedCreateFixture(t, "hlc4"))
}

// TestServedLifecycleCreateWritesEveryScalarField is PARKED on
// W-CreateRequest.Issue: seventeen of its members are the create vocabulary the
// wire publishes and five are not — spec_id, await_id, closed_by_session, and
// the creation stamp's created_at/created_by — and this client refuses each of
// those rather than dropping it. created_by is refused because the case names
// a creator OTHER than its actor; the server stamps the actor, so only that one
// value rides the stamp (TestServedCreateCarriesTheShapeBdCreateSends).
//
// The operation's own description says why the stamp is absent: a create whose
// stored creation time comes from the caller makes the row disagree with the
// journal entry that records it, and re-dating history is what an import is for.
// So this is not a gap waiting on a wave; it is the surface's decision, and the
// park will outlive the ones above.
func TestServedLifecycleCreateWritesEveryScalarField(t *testing.T) {
	skipKnownDivergence(t, "W-CreateRequest.Issue", createParkBead,
		"the case sets spec_id, await_id, closed_by_session, created_at and a created_by naming someone other than its actor; createIssue publishes none of them and the client refuses each per member rather than dropping it (asserted by TestCreateRefusesEveryMemberTheWireExcludes)")
	conformance.RunLifecycleCreateWritesEveryScalarField(t, t.Context(), newServedCreateFixture(t, "hlc5"))
}

// TestServedLifecycleCreateEchoesSubSecondTimestamps is PARKED on the same row
// as WritesEveryScalarField, and on the same member: the contract's precision
// pin is a caller-supplied created_at/updated_at with nanoseconds in it — its
// own preamble says the explicit arm is the pin and the auto-stamped arm only a
// bounded smoke check — and the creation stamp is exactly the member createIssue
// withholds by design. The client refuses Issue.CreatedAt before any request is
// sent, so there is no echo to measure; running the auto-stamped arm alone
// would assert a clock window the contract itself declines to make the pin.
func TestServedLifecycleCreateEchoesSubSecondTimestamps(t *testing.T) {
	skipKnownDivergence(t, "W-CreateRequest.Issue", createParkBead,
		"the case's precision pin is a caller-supplied created_at/updated_at, and the creation stamp is the member createIssue deliberately withholds; the client refuses Issue.CreatedAt before any request is sent, so the echo has nothing to echo (the refusal is asserted by TestCreateRefusesEveryMemberTheWireExcludes)")
	conformance.RunLifecycleCreateEchoesSubSecondTimestamps(t, t.Context(), newServedCreateFixture(t, "hlc6"))
}

// createParkBead is the bead the create-side parks cite. It is separate from
// parkBead — the write-side parks of the close/update wave — because these
// retire on different events: two of them wait on the wire growing a
// distinguishing refusal code, and the third waits on nothing at all.
const createParkBead = "ga-jbuyf"

// TestServedCreateRefusesAnAbsentTargetAsValidation is L-create-notfound's pin,
// and it RUNS.
//
// Both halves matter. The CLASSIFICATION half is the divergence: the refusal is
// ErrValidation alone and does not name the target, so the row retires loudly
// the day the wire grows a distinguishing code. The OUTCOME half is the reason
// the divergence is acceptable: the whole request fails and NOTHING is created —
// not the issue, and not the edge that named nothing.
//
// All three ways a request can name a target are driven here, including
// --parent: OpCreateIssue's own problem-code table (internal/httpapi/problem.go)
// documents NO 404 for ANY of them, on purpose — "there is no id in this path
// to have missed" applies to parent_id exactly as it does to a dependency's
// target_id or a waits-for's spawner_id, none of which are a resource this
// operation was asked to address. There is no asymmetry to carve a parent-only
// not_found out of without contradicting that design, so the three stay
// together on this one row.
func TestServedCreateRefusesAnAbsentTargetAsValidation(t *testing.T) {
	env := newServedEnv(t, "hlcn")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const missing = "hlcn-nosuchrow"
	for _, tc := range []struct {
		name    string
		id      string
		request issueops.CreateRequest
	}{
		{
			name:    "explicit dependency",
			id:      "hlcn-dep",
			request: issueops.CreateRequest{Dependencies: []issueops.CreateDependency{{TargetID: missing, Type: types.DepBlocks}}},
		},
		{
			name:    "waits-for spawner",
			id:      "hlcn-waits",
			request: issueops.CreateRequest{WaitsFor: &issueops.WaitsFor{SpawnerID: missing}},
		},
		{
			name:    "parent",
			id:      "hlcn-parent",
			request: issueops.CreateRequest{ParentID: missing},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.request
			req.Actor, req.ForceIDPrefix = "writer", true
			req.Issue = &issueops.Issue{
				ID: tc.id, Title: tc.name, Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
			}
			_, err := lifecycle.Create(ctx, req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("Create with an absent %s target: err = %v, want ErrValidation", tc.name, err)
			}
			if errors.Is(err, issueops.ErrNotFound) {
				t.Errorf("the refusal wraps ErrNotFound; L-create-notfound says the wire cannot express it — retire the row rather than the assertion")
			}
			if strings.Contains(err.Error(), missing) {
				t.Errorf("the refusal names the missing target %q; L-create-notfound says it cannot — retire the row", missing)
			}
			// The outcome half: neither plane holds the refused id.
			assertServedIssueRows(t, ctx, env, 0, tc.id)
			assertServedWispRows(t, ctx, env, 0, tc.id)
		})
	}
}

// TestServedCreateRefusesAForeignPrefixAsValidation is L-create-prefix's pin,
// and it RUNS.
//
// The guard is untouched — the id is refused, nothing is created, and the same
// request with ForceIDPrefix lands — and only the typed sentinel is lost. That
// last clause is what the row is about: both local front doors decide whether to
// re-offer the create with --force from errors.Is rather than from the message.
func TestServedCreateRefusesAForeignPrefixAsValidation(t *testing.T) {
	env := newServedEnv(t, "hlcp")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const foreign = "hlcpforeign-1"
	issue := func() *issueops.Issue {
		return &issueops.Issue{
			ID: foreign, Title: "foreign", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		}
	}

	_, err = lifecycle.Create(ctx, issueops.CreateRequest{Actor: "writer", Issue: issue()})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("unforced create at a foreign prefix: err = %v, want ErrValidation", err)
	}
	if errors.Is(err, storage.ErrPrefixMismatch) {
		t.Errorf("the refusal carries ErrPrefixMismatch; L-create-prefix says the wire cannot express it — retire the row rather than the assertion")
	}
	assertServedIssueRows(t, ctx, env, 0, foreign)

	// The half that makes the refusal a POLICY the caller can override rather
	// than a hard limit — and the half that proves the arm above refused on the
	// prefix rather than on the request being malformed.
	forced, err := lifecycle.Create(ctx, issueops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: issue()})
	if err != nil {
		t.Fatalf("forced create at a foreign prefix: %v", err)
	}
	if forced.Issue == nil || forced.Issue.ID != foreign {
		t.Fatalf("forced create answered %+v, want the requested id %q", forced.Issue, foreign)
	}
	assertServedIssueRows(t, ctx, env, 1, foreign)
}

// TestServedCreateAnswersTheRowAsStored pins the RESPONSE half of the create,
// which no request assertion can see: the row that comes back is the server's,
// not the request reflected.
//
// The three members it reads are the three only the server can supply — the
// MINTED id (the request named none), the DEFAULTED status (the request named
// none either) and the persisted creation stamp, which this operation
// deliberately does not let a caller set. A client that echoed its own request
// back would answer an empty id, an empty status and a zero time.
func TestServedCreateAnswersTheRowAsStored(t *testing.T) {
	env := newServedEnv(t, "hlcr2")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	created, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{Title: "minted", Priority: 2, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Issue == nil {
		t.Fatal("Create answered no issue")
	}
	if !strings.HasPrefix(created.Issue.ID, env.prefix+"-") {
		t.Errorf("minted id = %q, want one in the workspace's %q prefix", created.Issue.ID, env.prefix)
	}
	if created.Issue.Status != types.StatusOpen {
		t.Errorf("status = %q, want the defaulted %q", created.Issue.Status, types.StatusOpen)
	}
	if created.Issue.CreatedAt.IsZero() {
		t.Error("created_at is zero; the answer is the row as STORED, and this operation stamps it server-side")
	}
	if created.Issue.CreatedBy != "writer" {
		t.Errorf("created_by = %q, want the request's actor %q; the client never sends CreatedBy over the wire (internal/httpapi/create.go must stamp it from the actor)", created.Issue.CreatedBy, "writer")
	}

	// And the row really landed, read out of band.
	stored, err := env.getIssue(ctx, created.Issue.ID)
	if err != nil {
		t.Fatalf("read back %s: %v", created.Issue.ID, err)
	}
	if stored.Title != "minted" {
		t.Errorf("stored title = %q, want %q", stored.Title, "minted")
	}
	if stored.CreatedBy != "writer" {
		t.Errorf("stored created_by = %q, want %q", stored.CreatedBy, "writer")
	}
}

// TestServedCreateStampsCreatedByFromTheActorNotTheWire pins the server half of
// the created_by contract: createIssue publishes no created_by member, so the
// stored creator exists only because internal/httpapi's create.go stamps it from
// the request's actor. A different actor than TestServedCreateAnswersTheRowAsStored
// pins that the stamp tracks THIS request's actor, not a fixed default.
func TestServedCreateStampsCreatedByFromTheActorNotTheWire(t *testing.T) {
	env := newServedEnv(t, "hlcby")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	created, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: "a-different-actor",
		Issue: &issueops.Issue{Title: "stamped", Priority: 1, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Issue.CreatedBy != "a-different-actor" {
		t.Errorf("created_by = %q, want %q", created.Issue.CreatedBy, "a-different-actor")
	}

	stored, err := env.getIssue(ctx, created.Issue.ID)
	if err != nil {
		t.Fatalf("read back %s: %v", created.Issue.ID, err)
	}
	if stored.CreatedBy != "a-different-actor" {
		t.Errorf("stored created_by = %q, want %q", stored.CreatedBy, "a-different-actor")
	}
}

// TestServedCreateCarriesTheShapeBdCreateSends drives the request `bd create`
// actually builds (cmd/bd/create.go): Issue.CreatedBy names the actor, and
// IDPrefix carries the workspace's config.yaml prefix on every create, with no
// explicit id. Neither member has a place on the wire, and neither needs one —
// the server stamps created_by from the actor, and the override acts only on an
// explicit, unforced id — so refusing either would refuse every CLI create in a
// workspace whose config.yaml names a prefix.
func TestServedCreateCarriesTheShapeBdCreateSends(t *testing.T) {
	env := newServedEnv(t, "hlcli")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	created, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor:    "cli-user",
		Issue:    &issueops.Issue{Title: "from the cli", Priority: 2, IssueType: types.TypeTask, CreatedBy: "cli-user"},
		IDPrefix: env.prefix,
	})
	if err != nil {
		t.Fatalf("Create in bd create's shape = %v, want it served", err)
	}
	if !strings.HasPrefix(created.Issue.ID, env.prefix+"-") {
		t.Errorf("minted id = %q, want one in the workspace's %q prefix", created.Issue.ID, env.prefix)
	}
	stored, err := env.getIssue(ctx, created.Issue.ID)
	if err != nil {
		t.Fatalf("read back %s: %v", created.Issue.ID, err)
	}
	if stored.CreatedBy != "cli-user" {
		t.Errorf("stored created_by = %q, want the CreatedBy the request named", stored.CreatedBy)
	}
}

// TestServedCreateNamesTheOccupiedIDInItsRefusal is the served half of the
// message decoration the role makes.
//
// The wire's `already_exists` carries `param: "id"` and no id, because the
// request already said it. A caller of `bd create --id X` refused with a bare
// "already names a stored row" cannot act on it when the create was one of
// several, so the role puts the caller's own id back — the same fact a local
// backend's refusal carries in its own message.
func TestServedCreateNamesTheOccupiedIDInItsRefusal(t *testing.T) {
	env := newServedEnv(t, "hlco")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const occupied = "hlco-taken"
	seedServedIssue(t, ctx, env, occupied, types.StatusOpen)

	_, err = lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: "writer", ForceIDPrefix: true,
		Issue: &issueops.Issue{
			ID: occupied, Title: "squatter", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		},
	})
	if !errors.Is(err, issueops.ErrAlreadyExists) {
		t.Fatalf("create over an occupied id: err = %v, want ErrAlreadyExists", err)
	}
	if !strings.Contains(err.Error(), occupied) {
		t.Errorf("the refusal does not name the occupied id: %v", err)
	}
}
