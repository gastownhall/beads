package db

import (
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// The union stack's wiring of `bd ready --exclude-parents` (GH#6817): the
// parent set comes from the shared sqlbuild open-child statements (one per
// child family, the checked close gate's relationship) against this seam's
// own runner, keyed for the family being rendered, and folds into the ready
// WHERE as a batched id NOT IN. The flag-off arm pins the other half of the
// contract: an unflagged request must not run an open-child statement at all,
// so the exclusion's cost and semantics are both opt-in.
func TestBuildReadyWorkPredicatesExcludesParentsWithOpenChildren(t *testing.T) {
	// IncludeDeferred keeps the probe on the statements this case owns: no
	// deferred-parent probe, just the stale-flag scan plus (flagged only)
	// the open-child statements.
	unflagged := types.WorkFilter{Status: types.StatusOpen, IncludeDeferred: true}
	flagged := unflagged
	flagged.ExcludeParentsWithOpenChildren = true

	t.Run("flag off adds no open-child statement", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("SELECT t.id FROM issues t WHERE t.is_blocked = 1").
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		repo := &issueSQLRepositoryImpl{runner: db}

		preds, err := repo.buildReadyWorkPredicates(t.Context(), unflagged, issuesFilterTables)
		if err != nil {
			t.Fatalf("buildReadyWorkPredicates (unflagged): %v", err)
		}
		if strings.Contains(preds.whereSQL, "id NOT IN") {
			t.Errorf("the unflagged WHERE must exclude nothing; got %s", preds.whereSQL)
		}
		// sqlmock fails any query beyond the expected one, so a met
		// expectation set is also the proof the resolver never ran.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unflagged expectations: %v", err)
		}
	})

	t.Run("flag on resolves both child families and folds the set batched", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("is_blocked = 1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		// Durable leg first, then the wisp leg, both projecting the issue
		// parent column; xp-1 is named by both legs and must fold once.
		mock.ExpectQuery(`SELECT DISTINCT dependency\.depends_on_issue_id\s+FROM dependencies AS dependency`).
			WillReturnRows(sqlmock.NewRows([]string{"depends_on_issue_id"}).AddRow("xp-2").AddRow("xp-1"))
		mock.ExpectQuery(`SELECT DISTINCT dependency\.depends_on_issue_id\s+FROM wisp_dependencies AS dependency`).
			WillReturnRows(sqlmock.NewRows([]string{"depends_on_issue_id"}).AddRow("xp-1").AddRow("xp-3"))
		repo := &issueSQLRepositoryImpl{runner: db}

		preds, err := repo.buildReadyWorkPredicates(t.Context(), flagged, issuesFilterTables)
		if err != nil {
			t.Fatalf("buildReadyWorkPredicates (flagged): %v", err)
		}
		if !strings.Contains(preds.whereSQL, "id NOT IN (?,?,?)") {
			t.Fatalf("the flagged WHERE must fold the deduped parent set as a batched id NOT IN; got %s", preds.whereSQL)
		}
		start := -1
		for i, a := range preds.args {
			if toStringOrEmpty(a) == "xp-1" {
				start = i
				break
			}
		}
		if start < 0 || start+3 > len(preds.args) {
			t.Fatalf("args = %v, want the three excluded parents bound in order", preds.args)
		}
		got := []string{toStringOrEmpty(preds.args[start]), toStringOrEmpty(preds.args[start+1]), toStringOrEmpty(preds.args[start+2])}
		if got[0] != "xp-1" || got[1] != "xp-2" || got[2] != "xp-3" {
			t.Fatalf("the NOT IN placeholders must bind the deduped, sorted parents; got %v in %v", got, preds.args)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("flagged expectations: %v", err)
		}
	})
}

func toStringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}
