package issueops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestDepTargetKindFor pins the one rule every write path classifies a
// dependency target by (GH#8299, bd-6jk1y2): database evidence, not the prefix.
// A prefix is a hint about which rig minted an id, never proof of which
// database holds it — a shared ledger or a hydrated repo legitimately holds
// several prefixes — so a locally held target is its local kind whatever its
// prefix, and the prefix comparison survives only as the fallback for a target
// this database holds neither way.
func TestDepTargetKindFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		sourceID, targetID    string
		heldAsWisp, heldAsIssue bool
		want                  DepTargetKind
	}{
		// An explicit external reference is external without probing.
		{"explicit external ref", "bd-a", "external:JIRA-1", false, false, DepTargetExternal},
		{"explicit external ref even when held", "bd-a", "external:JIRA-1", true, true, DepTargetExternal},

		// Locally held targets are their local kind, whatever their prefix.
		{"same-prefix issue", "bd-a", "bd-b", false, true, DepTargetIssue},
		{"cross-prefix local issue", "bd-a", "hq-b", false, true, DepTargetIssue},
		{"cross-prefix local wisp", "bd-a", "hq-b", true, false, DepTargetWisp},
		{"wisp beats issue", "bd-a", "bd-b", true, true, DepTargetWisp},

		// A target this database holds neither way keeps the missing-target
		// policy of each caller: a foreign prefix stays external (the
		// multi-rig "add now, route later" shape), a same prefix stays an
		// issue so explicit adds refuse it and import paths skip it.
		{"missing foreign-prefix target", "bd-a", "hq-b", false, false, DepTargetExternal},
		{"missing same-prefix target", "bd-a", "bd-b", false, false, DepTargetIssue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DepTargetKindFor(tt.sourceID, tt.targetID, tt.heldAsWisp, tt.heldAsIssue)
			if got != tt.want {
				t.Errorf("DepTargetKindFor(%q, %q, wisp=%v, issue=%v) = %v, want %v",
					tt.sourceID, tt.targetID, tt.heldAsWisp, tt.heldAsIssue, got, tt.want)
			}
		})
	}
}

// TestClassifyDepTargetInTxCrossPrefixLocalTargetIsItsLocalKind is the
// in-transaction form of the rule: the probe order is wisp first, then issues,
// and a cross-prefix id this database holds is classified by where it is held,
// not by its prefix (GH#8299). Pre-fix this classifier answered External for
// every cross-prefix target before ever querying, so a same-database
// cross-prefix blocker was stored unresolvable and never gated readiness.
func TestClassifyDepTargetInTxCrossPrefixLocalTargetIsItsLocalKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		wispRow  bool
		issueRow bool
		want     DepTargetKind
	}{
		{"cross-prefix local issue", false, true, DepTargetIssue},
		{"cross-prefix local wisp", true, false, DepTargetWisp},
		{"cross-prefix and absent", false, false, DepTargetExternal},
		{"same-prefix local issue", false, true, DepTargetIssue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)

			// Both planes are probed on every classification — the rule
			// takes both verdicts and ranks them (wisp beats issue), so a
			// wisp hit does not short-circuit the issues read.
			wispRows := sqlmock.NewRows([]string{"issue_type"})
			if tc.wispRow {
				wispRows.AddRow("task")
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
				WithArgs("hq-b").
				WillReturnRows(wispRows)
			issueRows := sqlmock.NewRows([]string{"issue_type"})
			if tc.issueRow {
				issueRows.AddRow("task")
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
				WithArgs("hq-b").
				WillReturnRows(issueRows)

			dep := &types.Dependency{IssueID: "bd-a", DependsOnID: "hq-b", Type: types.DepBlocks}
			got, err := ClassifyDepTargetInTx(context.Background(), tx, dep)
			if err != nil {
				t.Fatalf("ClassifyDepTargetInTx(bd-a -> hq-b) = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("ClassifyDepTargetInTx(bd-a -> hq-b) = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestClassifyDepTargetInTxForeignTargetSkipsProbes pins that an explicit
// "external:" reference is classified without touching the database.
func TestClassifyDepTargetInTxForeignTargetSkipsProbes(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	dep := &types.Dependency{IssueID: "bd-a", DependsOnID: "external:JIRA-4471", Type: types.DepRelated}
	got, err := ClassifyDepTargetInTx(context.Background(), tx, dep)
	if err != nil {
		t.Fatalf("ClassifyDepTargetInTx(external ref) = %v, want nil", err)
	}
	if got != DepTargetExternal {
		t.Errorf("ClassifyDepTargetInTx(external ref) = %v, want %v", got, DepTargetExternal)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v (an external ref must not probe)", err)
	}
}

// TestClassifyDepTargetInTxProbeErrorsPropagate pins the failure contract of
// the lookup (GH#8299): only sql.ErrNoRows and the optional wisp plane's
// absence mean "not held". A probe that swallowed its errors would
// misclassify a failed read as a missing target and route a local edge to
// depends_on_external — the silent misclassification this rule exists to
// prevent, and the reason IsActiveWispInTx (which suppresses every error) is
// not the probe here.
func TestClassifyDepTargetInTxProbeErrorsPropagate(t *testing.T) {
	t.Parallel()

	probeErr := errors.New("connection refused")
	newDep := func() *types.Dependency {
		return &types.Dependency{IssueID: "bd-a", DependsOnID: "hq-b", Type: types.DepBlocks}
	}

	t.Run("wisps probe error", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
			WithArgs("hq-b").
			WillReturnError(probeErr)
		if _, err := ClassifyDepTargetInTx(context.Background(), tx, newDep()); err == nil {
			t.Fatal("ClassifyDepTargetInTx swallowed the wisps probe error, want it propagated")
		}
	})

	t.Run("issues probe error", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
			WithArgs("hq-b").
			WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
			WithArgs("hq-b").
			WillReturnError(probeErr)
		if _, err := ClassifyDepTargetInTx(context.Background(), tx, newDep()); err == nil {
			t.Fatal("ClassifyDepTargetInTx swallowed the issues probe error, want it propagated")
		}
	})

	t.Run("missing wisps table tolerated", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		// A fresh clone can lose its dolt-ignored wisps plane (bd-cif6y4);
		// that absence means "no wisp targets", not a failed probe.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
			WithArgs("hq-b").
			WillReturnError(fmt.Errorf("Error 1146 (42S02): table 'beads.wisps' doesn't exist"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
			WithArgs("hq-b").
			WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
		got, err := ClassifyDepTargetInTx(context.Background(), tx, newDep())
		if err != nil {
			t.Fatalf("ClassifyDepTargetInTx over a missing wisps table = %v, want nil", err)
		}
		if got != DepTargetIssue {
			t.Errorf("ClassifyDepTargetInTx over a missing wisps table = %v, want %v (the issues row is held)", got, DepTargetIssue)
		}
	})
}

// TestAddDependencyInTxStoresCrossPrefixLocalTargetInTypedColumn pins the
// write path's use of the rule (GH#8299): `bd dep add bd-a hq-b` on a database
// that holds hq-b as an issue inserts the edge into depends_on_issue_id with
// depends_on_external NULL. Pre-fix the classifier answered External before
// probing, so this INSERT named depends_on_external instead.
func TestAddDependencyInTxStoresCrossPrefixLocalTargetInTypedColumn(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// Classification: target hq-b is not a wisp but is held in issues.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
		WithArgs("hq-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("hq-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// Source read.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// Blocking edges run the hierarchy walk (now including cross-prefix
	// local targets) and the cycle check. Neither walk can connect bd-a and
	// hq-b here, so both come back empty.
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("bd-a", "hq-b").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("hq-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery("WITH RECURSIVE reachable").
		WithArgs("hq-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"reachable"}).AddRow(0))
	// No existing edge.
	mock.ExpectQuery("SELECT type, metadata, thread_id FROM dependencies").
		WithArgs("bd-a", "hq-b").
		WillReturnRows(sqlmock.NewRows([]string{"type", "metadata", "thread_id"}))
	// The edge row itself: typed target column, external column NULL.
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata, thread_id)")).
		WithArgs(depid.New("bd-a", "hq-b"), "bd-a", "hq-b", types.DepBlocks, "writer", "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The affected-set expansion walks parent-child children of the source
	// in both dep tables; the source has none.
	mock.ExpectQuery("SELECT issue_id FROM dependencies").
		WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery("SELECT issue_id FROM wisp_dependencies").
		WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	// A blocks edge marks its source's blocked state directly. The EXISTS
	// gate re-reads the (now locally classified) target on the same session.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issues s SET s.is_blocked = 1")).
		WithArgs("bd-a", "hq-b").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The affected set reduced to empty, the durable journal is not enabled
	// on a bare transaction, and versioned history is not active on one, so
	// the recompute/journal/version tail below the insert is a no-op.

	dep := &types.Dependency{IssueID: "bd-a", DependsOnID: "hq-b", Type: types.DepBlocks}
	// The bool AddDependencyInTx reports is whether an audit event row was
	// written, not whether an edge was inserted (EmitEvent is unset here), so
	// success is a nil error plus every expectation — the typed-column INSERT
	// above — being consumed.
	_, err := AddDependencyInTx(context.Background(), tx, dep, "writer", AddDependencyOpts{
		SourceTable: "issues",
		WriteTable:  "dependencies",
	})
	if err != nil {
		t.Fatalf("AddDependencyInTx(bd-a -> hq-b) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAddDependencyInTxRefusesMissingSamePrefixTargetWithTypedError pins the
// missing-target half of the preserved behavior (GH#8299): a same-prefix
// target no local row answers falls back to DepTargetIssue in classification,
// and that fallback is NOT evidence of presence — the existence read still
// runs and its miss is the typed MissingDependencyTarget refusal, not an FK
// violation surfacing from the INSERT. The container-backed twin is
// TestAddDependency_SamePrefix_RequiresTargetExistence (internal/storage/dolt).
func TestAddDependencyInTxRefusesMissingSamePrefixTargetWithTypedError(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// Classification probes both planes and finds no row either way.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
		WithArgs("bd-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))
	// Source read.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// The fallback classification is not validated, so the target's
	// existence read runs — and misses.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))

	dep := &types.Dependency{IssueID: "bd-a", DependsOnID: "bd-b", Type: types.DepBlocks}
	_, err := AddDependencyInTx(context.Background(), tx, dep, "writer", AddDependencyOpts{
		SourceTable: "issues",
		WriteTable:  "dependencies",
	})
	if err == nil {
		t.Fatal("AddDependencyInTx(bd-a -> missing bd-b) = nil, want the missing-target refusal")
	}
	var notFound *publicops.DependencyEndpointNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want DependencyEndpointNotFoundError (not a raw FK or SQL error)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAddDependencyInTxRefusesBlockingACrossPrefixAncestor pins the explicit
// add path's half of the hierarchy rule (GH#8299): a blocker this database
// holds is checked for hierarchy conflicts whatever its prefix. Pre-fix the
// cross-prefix target classified External before any read, the hierarchy walk
// never ran, and `bd dep add <child> <cross-prefix ancestor> --type blocks`
// committed a self-blocking hierarchy edge.
func TestAddDependencyInTxRefusesBlockingACrossPrefixAncestor(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// Classification: hq-parent is not a wisp but is held in issues.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM wisps WHERE id = ?")).
		WithArgs("hq-parent").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("hq-parent").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// Source read.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-child").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// The first ancestry walk finds the blocker: hq-parent IS bd-child's
	// ancestor in this database's own parent-child graph.
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("bd-child", "hq-parent").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

	dep := &types.Dependency{IssueID: "bd-child", DependsOnID: "hq-parent", Type: types.DepBlocks}
	_, err := AddDependencyInTx(context.Background(), tx, dep, "writer", AddDependencyOpts{
		SourceTable: "issues",
		WriteTable:  "dependencies",
	})
	var conflict *domain.DependencyHierarchyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("AddDependencyInTx(bd-child blocks cross-prefix ancestor) = %v, want DependencyHierarchyConflictError", err)
	}
	if conflict.BlockerIsAncestor != true {
		t.Errorf("conflict = %#v, want the blocker reported as the source's ancestor", conflict)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
