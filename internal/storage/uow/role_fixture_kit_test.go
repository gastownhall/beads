package uow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// roleFixtureKit is the unit-of-work backend's answer to the hooks every
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
	// The event is not incidental. This backend has no event-free route to the
	// dependency tables — both domain.DependencyUseCase.add and AddDependencies
	// hard-code EmitEvent: true — while the two store backends default the
	// other way (dolt/dependencies.go:20-26 "adds a dependency between two
	// issues WITHOUT recording a dependency_added event"). Rather than let the
	// same seed leave different rows behind on different backends, all three
	// kits emit. A contract case that counts events must therefore take a DELTA
	// around the operation under test; an absolute count taken after seeding
	// edges counts the seeds too.
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
	// pass quietly. It is non-nil here: see
	// TestUOWRoleFixtureKitCountHistoryMovesWithACommit for the evidence.
	CountHistory func(context.Context) (int, error)
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

// newUOWRoleFixtureProvider boots one provider for a whole role suite and
// points its workspace prefix at the ids that suite will seed.
//
// ONE PROVIDER PER ROLE SUITE, not per case. Each call boots a real Dolt
// sql-server, so a role that builds a fresh provider per test function pays
// that boot for every case; the conformance fixtures were designed for sharing
// precisely so it does not have to (IssuePrefix namespaces the seeded ids).
//
// NO t.Parallel IN A SUITE THAT MEASURES COUNT DELTAS ON THIS BACKEND. The
// server-backed store gives each test its own copy-on-write branch; this one
// does not. dolt_log and the event tables are database-global here, so a
// parallel subtest sharing the provider corrupts another subtest's before/after
// arithmetic.
func newUOWRoleFixtureProvider(t *testing.T, ctx context.Context, prefix string) UnitOfWorkProvider {
	t.Helper()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: set issue prefix", uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", prefix)
	}); err != nil {
		t.Fatalf("set issue_prefix to %q: %v", prefix, err)
	}
	return provider
}

// newUOWRoleFixtureKit builds the kit for a provider from
// newUOWRoleFixtureProvider. Every hook is one unit of work: this backend has
// no ambient connection a test can write through, so seeding is the same
// RunTx/RunTxRead shape the production code uses.
func newUOWRoleFixtureKit(provider UnitOfWorkProvider, prefix string) roleFixtureKit {
	seed := func(wisp bool) func(context.Context, *types.Issue, string) error {
		return func(ctx context.Context, issue *types.Issue, actor string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				params := domain.CreateIssueParams{
					Issue:      issue,
					ExplicitID: issue.ID,
					Labels:     append([]string(nil), issue.Labels...),
					CreateOnly: true,
				}
				var err error
				if wisp {
					_, err = uw.IssueUseCase().CreateWisp(ctx, params, actor)
				} else {
					_, err = uw.IssueUseCase().CreateIssue(ctx, params, actor)
				}
				return "seed " + issue.ID, err
			})
		}
	}
	queryScalar := func(ctx context.Context, query string, args []any, dest ...any) error {
		row, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]any, error) {
			result, err := uw.RawSQLUseCase().Query(ctx, query, args...)
			if err != nil {
				return nil, err
			}
			if len(result.Rows) != 1 {
				return nil, fmt.Errorf("query %q returned %d rows, want 1", query, len(result.Rows))
			}
			return result.Rows[0], nil
		})
		if err != nil {
			return err
		}
		if len(row) != len(dest) {
			return fmt.Errorf("query %q returned %d columns, want %d", query, len(row), len(dest))
		}
		for i, target := range dest {
			if err := scanRawSQLValue(target, row[i]); err != nil {
				return fmt.Errorf("query %q column %d: %w", query, i, err)
			}
		}
		return nil
	}
	return roleFixtureKit{
		IssuePrefix: prefix,
		CreateIssue: seed(false),
		CreateWisp:  seed(true),
		AddDependency: func(ctx context.Context, dep *types.Dependency, actor string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				// The source-routed bulk verb, not the plane-pinned single one:
				// it is what makes this seed land the edge in the same plane the
				// two store backends' AddDependency lands it in.
				_, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{})
				return "seed dependency " + dep.IssueID + " -> " + dep.DependsOnID, err
			})
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "set " + key, uw.ConfigUseCase().SetConfig(ctx, key, value)
			})
		},
		QueryScalar: queryScalar,
		CountHistory: func(ctx context.Context) (int, error) {
			var entries int
			err := queryScalar(ctx, "SELECT COUNT(*) FROM dolt_log", nil, &entries)
			return entries, err
		},
	}
}

// skipKnownDivergence parks one conformance case on the backend that disagrees
// with the leaf contract's doc comment.
//
// The contract case asserts what the doc promises, so a genuine disagreement is
// a behaviour-unification decision for the owner rather than something a test
// slice may settle by weakening the assertion. Parking at the WIRING site (never
// inside the shared Run function) keeps the case running and passing on the
// backends that agree, so their behaviour is pinned the day the divergence is
// found. The "KNOWN DIVERGENCE" prefix is literal so `grep -r "KNOWN DIVERGENCE"`
// finds every parked case, and beadID names the child of bd-yby99 that records
// the three-way observed behaviour.
func skipKnownDivergence(t *testing.T, beadID, reason string) {
	t.Helper()
	t.Skipf("KNOWN DIVERGENCE %s: %s", beadID, reason)
}

// TestScanRawSQLValueCoversTheContractDestinations pins the scan-destination
// set the role contracts depend on. It needs no database, so a wiring that
// scans into an unsupported type is caught here in milliseconds rather than
// after a Dolt server boot.
func TestScanRawSQLValueCoversTheContractDestinations(t *testing.T) {
	stamp := time.Date(2026, 8, 2, 19, 49, 5, 123456000, time.UTC)

	var (
		gotInt     int
		gotInt64   int64
		gotBool    bool
		gotFloat   float64
		gotString  string
		gotTime    time.Time
		gotTimeRaw time.Time
	)
	for _, test := range []struct {
		name  string
		value any
		dest  any
		want  any
		got   func() any
	}{
		{"int from bytes", []byte("3"), &gotInt, 3, func() any { return gotInt }},
		{"int64 from string", "9007199254740993", &gotInt64, int64(9007199254740993), func() any { return gotInt64 }},
		{"bool from tinyint", int64(1), &gotBool, true, func() any { return gotBool }},
		{"bool from text", []byte("false"), &gotBool, false, func() any { return gotBool }},
		{"float64", "2.5", &gotFloat, 2.5, func() any { return gotFloat }},
		{"string from NULL", nil, &gotString, "", func() any { return gotString }},
		{"time from driver value", stamp, &gotTimeRaw, stamp, func() any { return gotTimeRaw }},
		{"time from text", "2026-08-02 19:49:05.123456", &gotTime, stamp, func() any { return gotTime }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := scanRawSQLValue(test.dest, test.value); err != nil {
				t.Fatalf("scanRawSQLValue(%T, %v): %v", test.dest, test.value, err)
			}
			if got := test.got(); got != test.want {
				t.Fatalf("scanned %#v, want %#v", got, test.want)
			}
		})
	}

	// NULL must fail loudly for every destination that cannot represent it,
	// rather than decay to a zero a count case would read as a real answer.
	for _, dest := range []any{&gotInt, &gotInt64, &gotBool, &gotFloat, &gotTime} {
		if err := scanRawSQLValue(dest, nil); err == nil {
			t.Fatalf("scanRawSQLValue(%T, NULL) = nil error, want a refusal", dest)
		}
	}
	if err := scanRawSQLValue(new(uint), 1); err == nil {
		t.Fatal("scanRawSQLValue accepted an unsupported destination")
	}
}

// TestUOWRoleFixtureKitHooksAreUsable is the scaffolding's own tripwire: every
// hook the role fixtures compose is exercised once here, so a kit broken by a
// signature change fails in one obvious place instead of five role suites.
func TestUOWRoleFixtureKitHooksAreUsable(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "kit")

	assertRoleFixtureKitHooksAreUsable(t, ctx, newUOWRoleFixtureKit(provider, "kit"))
}

// assertRoleFixtureKitHooksAreUsable drives every hook once and checks the rows
// landed where the field docs say they land. It pins the FIXTURE, not a role,
// and is deliberately a byte-for-byte sibling of the same helper in the other
// two backends' kit files so the three can be diffed.
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
}

// TestUOWRoleFixtureKitCountHistoryMovesWithACommit answers the one open
// research question the design left to the scaffolding slice: can this
// backend's fixture observe dolt_log at all?
//
// It can. RawSQLUseCase.Query is an unfiltered pass-through to the transaction's
// runner (domain/db/raw_sql.go — no table allow-list anywhere in the chain), the
// provider is a real Dolt sql-server, and every RunTx that returns a non-empty
// commit message ends in CALL DOLT_COMMIT (uow/doltserver_tx.go:23-31). So a
// versioned unit of work is visible as a dolt_log row from inside a later
// read-only unit of work, which is exactly what the role contracts' "at most one
// history entry per call, none when nothing landed" clause needs.
//
// The consequence of that answer is what matters: CountHistory is NON-NIL on all
// three fixtures, so the entry-per-call cases run on all three and the clause is
// pinned on the uow body as well as on the body dolt and embeddeddolt share.
// Had the answer been no, those cases would have skipped here and the clause
// would have been pinned only on two implementations that share ONE body — one
// vote, not two — on exactly the axis where the claim-message divergence lived
// (uow/ready_claimer.go:60-66).
func TestUOWRoleFixtureKitCountHistoryMovesWithACommit(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "kithist")
	kit := newUOWRoleFixtureKit(provider, "kithist")

	before, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	if before < 1 {
		t.Fatalf("history entries = %d before any test write, want at least the initializing commit", before)
	}
	if err := kit.SetConfig(ctx, "role_fixture_kit_history_probe", "on"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	after, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before+1 {
		t.Fatalf("history entries went %d -> %d across one committed unit of work, want exactly one more", before, after)
	}

	// A unit of work that writes nothing must not move the log either — the
	// "none when nothing landed" half of the same clause, proven on the fixture
	// so a role case can rely on it.
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "", nil
	}); err != nil {
		t.Fatalf("empty unit of work: %v", err)
	}
	idle, err := kit.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after an empty unit of work: %v", err)
	}
	if idle != after {
		t.Fatalf("history entries went %d -> %d across a unit of work that committed nothing, want no change", after, idle)
	}
}
