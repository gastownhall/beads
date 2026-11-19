package db

import (
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// The union stack's wiring of `bd ready --exclude-parents` (GH#6817): the
// parent set comes from the SHARED issueops resolver against this seam's own
// runner — keyed for the family being rendered — and folds into the ready
// WHERE as a batched id NOT IN. The flag-off arm pins the other half of the
// contract: an unflagged request must not run the open-child statement at all,
// so the exclusion's cost and semantics are both opt-in.
func TestBuildReadyWorkPredicatesExcludesParentsWithOpenChildren(t *testing.T) {
	// IncludeDeferred keeps the probe on the statements this case owns: no
	// deferred-parent probe, just the stale-flag scan plus (flagged only)
	// the shared open-child resolver.
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

	t.Run("flag on resolves the shared set and folds it batched", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("is_blocked = 1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery("SELECT DISTINCT dependency").
			WillReturnRows(sqlmock.NewRows([]string{"depends_on_issue_id"}).AddRow("xp-2").AddRow("xp-1"))
		repo := &issueSQLRepositoryImpl{runner: db}

		preds, err := repo.buildReadyWorkPredicates(t.Context(), flagged, issuesFilterTables)
		if err != nil {
			t.Fatalf("buildReadyWorkPredicates (flagged): %v", err)
		}
		if !strings.Contains(preds.whereSQL, "id NOT IN (?,?)") {
			t.Fatalf("the flagged WHERE must fold the parent set as a batched id NOT IN; got %s", preds.whereSQL)
		}
		if n := len(preds.args); n < 2 {
			t.Fatalf("args = %v, want at least the two excluded parents", preds.args)
		} else if got := []string{toStringOrEmpty(preds.args[n-2]), toStringOrEmpty(preds.args[n-1])}; got[0] != "xp-1" || got[1] != "xp-2" {
			t.Fatalf("the NOT IN placeholders must bind the sorted parents; tail = %v", got)
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
