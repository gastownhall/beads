package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// roleFixtureKit is the server-backed store's answer to the hooks every
// issueops role fixture in package conformance needs. A role's wiring file
// composes one of these with the accessor under test and the IssuePrefix, so
// the seeding and scalar-query plumbing is written once per backend instead of
// once per role.
//
// The field names and signatures are IDENTICAL across the three backends'
// kits, and identical to the fields the conformance fixtures declare, so a kit
// closure is assignable to any role fixture with no adapter in between. That
// sameness is the point of the type; changing a signature here is a change to
// all three backends and to every role wiring at once.
//
// FROZEN SURFACE. This file is owned by the scaffolding slice (bd-kue5t) and
// no role slice edits it. A role that needs a hook this kit does not expose
// routes the addition through a follow-up commit against that bead, so the
// three kits never drift apart in a worktree.
//
// NAMING CONVENTION FOR EVERYTHING BUILT ON THIS KIT: every unexported helper a
// role slice adds — in package conformance and in the per-backend wiring files —
// is ROLE-PREFIXED (seedRelationsAnchor, assertCommenterEventCount), the way the
// existing contract already names seedDependencyEditorIssue and
// assertDependencyEdgeCount. Five slices add files to one package from separate
// worktrees; two of them each defining a bare seedIssue would compile alone and
// break combined. The scaffolding's own helpers are prefixed roleFixtureKit for
// the same reason.
type roleFixtureKit struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several
	// assertions can share one database.
	IssuePrefix string
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than a flag because the three backends reach the two planes
	// through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, routed to the plane the edge's SOURCE lives
	// in, and RECORDS a dependency_added event.
	//
	// The event is not incidental. The unit-of-work backend has no event-free
	// route to the dependency tables — both domain.DependencyUseCase.add and
	// AddDependencies hard-code EmitEvent: true — while the two store backends
	// default the other way (dolt/dependencies.go:20-26 "adds a dependency
	// between two issues WITHOUT recording a dependency_added event"). Rather
	// than let the same seed leave different rows behind on different backends,
	// all three kits emit. A contract case that counts events must therefore
	// take a DELTA around the operation under test; an absolute count taken
	// after seeding edges counts the seeds too.
	//
	// SPEC-GAP bd-yby99.1: no issueops leaf doc says whether a structural
	// dependency insert records an event, so the kits normalise rather than
	// pick a winner. The owner adjudicates it with the rest of the batch.
	AddDependency func(context.Context, *types.Dependency, string) error
	// SetConfig writes one workspace config key, which is how a case installs
	// the vocabulary (excluded types, default limits) a request is read against.
	SetConfig func(context.Context, string, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test, so a case can assert on a query that is
	// expected to fail.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has,
	// for the "at most one entry per call, none when nothing landed" clause the
	// role contracts state. Cases take it before and after rather than reading
	// the top of the log, because two commits made inside one second tie on
	// date and their relative order is not something to rely on.
	//
	// A nil CountHistory means "this backend cannot observe history here". A
	// case that needs it must then skip LOUDLY with that reason rather than
	// pass quietly. It is non-nil on all three backends today; see
	// uow/role_fixture_kit_test.go for the evidence on the awkward one.
	CountHistory func(context.Context) (int, error)
	// CountHistoryMatching is CountHistory scoped to what the entries READ: it
	// counts the entries whose message matches pattern, where "" means every
	// entry and anything else is a SQL LIKE pattern the backend applies with
	// its own escaping.
	//
	// Three role cases assert on a MESSAGE rather than on a bare count — "an
	// entry naming this wisp", "an entry reading the caller's provenance
	// label" — and before this hook existed they reached past the fixture into
	// `dolt_log` through QueryScalar to get one. That was the only Dolt-engine
	// dependency left in the role contracts.
	//
	// It does NOT replace CountHistory: a backend can know how long its
	// history is without being able to match on the text of an entry, and 25
	// cases run on the narrow hook alone. Where both are available CountHistory
	// is DEFINED as CountHistoryMatching(ctx, "") so the two cannot disagree.
	// Nil carries the same meaning as a nil CountHistory, and the cases that
	// need it skip LOUDLY.
	CountHistoryMatching func(context.Context, string) (int, error)
}

// roleFixtureKitComposesConformanceFixtures is the compile-time half of the
// kit's promise: every hook is assignable to the fixture field of the same
// name with NO adapter in between. A signature drifting apart from the
// conformance fixtures breaks here, in the frozen file that owns the
// signatures, instead of in five role wirings at once.
var roleFixtureKitComposesConformanceFixtures = func(kit roleFixtureKit) (conformance.DependencyEditorFixture, conformance.IssueOperationsStagingFixture) {
	return conformance.DependencyEditorFixture{
			IssuePrefix: kit.IssuePrefix,
			CreateIssue: kit.CreateIssue,
			CreateWisp:  kit.CreateWisp,
			QueryScalar: kit.QueryScalar,
		}, conformance.IssueOperationsStagingFixture{
			IssuePrefix:   kit.IssuePrefix,
			CreateIssue:   kit.CreateIssue,
			AddDependency: kit.AddDependency,
			SetConfig:     kit.SetConfig,
			QueryScalar:   kit.QueryScalar,
		}
}

// newDoltRoleFixtureKit builds the kit for a store from setupTestStore. The
// store's own create routes an ephemeral issue to the wisps plane, so the two
// seed verbs differ only by the flag already on the issue.
func newDoltRoleFixtureKit(store *DoltStore, prefix string) roleFixtureKit {
	countHistoryMatching := func(ctx context.Context, pattern string) (int, error) {
		query := "SELECT COUNT(*) FROM dolt_log"
		var args []any
		if pattern != "" {
			query += " WHERE message LIKE ?"
			args = append(args, pattern)
		}
		var entries int
		err := store.db.QueryRowContext(ctx, query, args...).Scan(&entries)
		return entries, err
	}
	return roleFixtureKit{
		IssuePrefix: prefix,
		CreateIssue: store.CreateIssue,
		CreateWisp:  store.CreateIssue,
		AddDependency: func(ctx context.Context, dep *types.Dependency, actor string) error {
			return store.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true})
		},
		SetConfig: store.SetConfig,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest...)
		},
		CountHistoryMatching: countHistoryMatching,
		CountHistory: func(ctx context.Context) (int, error) {
			return countHistoryMatching(ctx, "")
		},
	}
}

// TestDoltRoleFixtureKitHooksAreUsable is the scaffolding's own tripwire: every
// hook the role fixtures compose is exercised once here, so a kit broken by a
// signature change fails in one obvious place instead of five role suites.
func TestDoltRoleFixtureKitHooksAreUsable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	kit := newDoltRoleFixtureKit(store, "test")

	assertRoleFixtureKitHooksAreUsable(t, ctx, kit)
}

// assertRoleFixtureKitHooksAreUsable drives every hook once and checks the rows
// landed where the field docs say they land. It is shared by the kit tripwire
// and is deliberately not a conformance case: it pins the FIXTURE, not a role.
func assertRoleFixtureKitHooksAreUsable(t *testing.T, ctx context.Context, kit roleFixtureKit) {
	t.Helper()
	issue := kit.IssuePrefix + "-kit-issue"
	target := kit.IssuePrefix + "-kit-target"
	wisp := kit.IssuePrefix + "-kit-wisp"

	for _, id := range []string{issue, target} {
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("kit.CreateIssue(%s): %v", id, err)
		}
	}
	if err := kit.CreateWisp(ctx, &types.Issue{
		ID: wisp, Title: wisp, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("kit.CreateWisp(%s): %v", wisp, err)
	}
	if err := kit.AddDependency(ctx, &types.Dependency{
		IssueID: issue, DependsOnID: target, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("kit.AddDependency: %v", err)
	}
	if err := kit.SetConfig(ctx, "role_fixture_kit_probe", "on"); err != nil {
		t.Fatalf("kit.SetConfig: %v", err)
	}

	var issues, wisps, edges int
	if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id IN (?, ?)", []any{issue, target}, &issues); err != nil {
		t.Fatalf("kit.QueryScalar issues: %v", err)
	}
	if issues != 2 {
		t.Fatalf("seeded durable issues = %d, want 2", issues)
	}
	if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{wisp}, &wisps); err != nil {
		t.Fatalf("kit.QueryScalar wisps: %v", err)
	}
	if wisps != 1 {
		t.Fatalf("seeded wisps = %d, want 1 — CreateWisp did not reach the ephemeral plane", wisps)
	}
	if err := kit.QueryScalar(ctx,
		// The target's own class decides which typed column holds it, so the
		// contracts resolve it through this COALESCE rather than one column.
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND "+
			"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		[]any{issue, target}, &edges); err != nil {
		t.Fatalf("kit.QueryScalar dependencies: %v", err)
	}
	if edges != 1 {
		t.Fatalf("seeded edges = %d, want 1", edges)
	}
	var configured string
	if err := kit.QueryScalar(ctx, "SELECT value FROM config WHERE `key` = ?", []any{"role_fixture_kit_probe"}, &configured); err != nil {
		t.Fatalf("kit.QueryScalar config: %v", err)
	}
	if configured != "on" {
		t.Fatalf("configured value = %q, want %q", configured, "on")
	}

	if kit.CountHistory == nil {
		t.Fatal("kit.CountHistory is nil — this backend cannot observe history, which the role contracts' entry-per-call clause needs")
	}
	entries, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("kit.CountHistory: %v", err)
	}
	if entries < 1 {
		t.Fatalf("history entries = %d, want at least the initializing commit", entries)
	}

	if kit.CountHistoryMatching == nil {
		t.Fatal("kit.CountHistoryMatching is nil — this backend cannot observe history BY MESSAGE, which the role contracts' provenance and wisp-naming clauses need")
	}
	all, err := kit.CountHistoryMatching(ctx, "")
	if err != nil {
		t.Fatalf("kit.CountHistoryMatching(all): %v", err)
	}
	if all != entries {
		t.Fatalf("CountHistoryMatching with an empty pattern = %d, want the %d CountHistory reports — the empty pattern means every entry", all, entries)
	}
	// The pattern has to FILTER. A hook that accepted one and answered the
	// total anyway would leave every message-scoped assertion vacuous.
	none, err := kit.CountHistoryMatching(ctx, "%no entry in this fixture reads this%")
	if err != nil {
		t.Fatalf("kit.CountHistoryMatching(miss): %v", err)
	}
	if none != 0 {
		t.Fatalf("CountHistoryMatching of a message no entry carries = %d, want 0 — the pattern must narrow the count", none)
	}
}

// TestDoltRoleFixtureKitCountHistoryMovesWithACommit is the other half of the
// history capability: CountHistory has to MOVE, not merely answer. The seed
// hooks are the wrong probe for it — only some of them version what they write
// (the embedded store's withConn takes a SQL commit, not a Dolt one) — so each
// backend proves the delta with its own committing verb.
func TestDoltRoleFixtureKitCountHistoryMovesWithACommit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	kit := newDoltRoleFixtureKit(store, "test")

	seedIssues(ctx, t, store, "test-kit-hist-a", "test-kit-hist-b")
	before, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	if err := kit.AddDependency(ctx, &types.Dependency{
		IssueID: "test-kit-hist-a", DependsOnID: "test-kit-hist-b", Type: types.DepBlocks,
	}, "writer"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	after, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before+1 {
		t.Fatalf("history entries went %d -> %d across one versioned write, want exactly one more", before, after)
	}
}
