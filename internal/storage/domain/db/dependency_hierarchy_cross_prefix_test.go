package db

import (
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// TestValidateBlockingHierarchySkipsOnlyAbsentTargets pins the serve-mode half
// of the GH#8299 hierarchy gate. ValidateBlockingHierarchy is the decision the
// unit-of-work batch end gate delegates to, and it must skip a target by
// ABSENCE, not by prefix: a foreign-prefix id with a local issue row takes the
// ancestry walk like any same-prefix target, while only a target absent from
// both local tables is external and outside every local hierarchy. On base the
// prefix-only skip returned nil before any read, so a blocking edge on a local
// cross-prefix ancestor passed every gate that consulted this validator.
func TestValidateBlockingHierarchySkipsOnlyAbsentTargets(t *testing.T) {
	dep := &types.Dependency{IssueID: "aa-gate", DependsOnID: "bb-anc", Type: types.DepBlocks}

	t.Run("present cross-prefix target takes the ancestry walk", func(t *testing.T) {
		mock, repo := newMockDependencyRepo(t)

		// Classification: no active wisp, and the foreign-prefix target
		// EXISTS in issues — the presence fact that makes it a local edge.
		mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
			WithArgs("bb-anc").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\? LIMIT 1").
			WithArgs("bb-anc").
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

		// The ancestry walk runs both directions; neither connects the pair.
		mock.ExpectQuery("WITH RECURSIVE").
			WithArgs("aa-gate", "bb-anc").
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		mock.ExpectQuery("WITH RECURSIVE").
			WithArgs("bb-anc", "aa-gate").
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))

		if err := repo.ValidateBlockingHierarchy(t.Context(), dep); err != nil {
			t.Fatalf("ValidateBlockingHierarchy for a local cross-prefix target with no ancestry: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("absent cross-prefix target stays outside the gate", func(t *testing.T) {
		mock, repo := newMockDependencyRepo(t)

		// Classification finds nothing: no wisp, no issue row, so the target
		// is external whatever its prefix, and no ancestry walk may run —
		// the scripted expectations end at the two probes.
		mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
			WithArgs("bb-anc").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\? LIMIT 1").
			WithArgs("bb-anc").
			WillReturnError(sql.ErrNoRows)

		if err := repo.ValidateBlockingHierarchy(t.Context(), dep); err != nil {
			t.Fatalf("ValidateBlockingHierarchy for an absent cross-prefix target: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})
}
