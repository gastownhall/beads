package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEnforceClaimPolicyInTx pins the claim guard's refusal: the SAME live
// direct-blocker predicate close policy applies (isBlockedByInTx — the stored
// is_blocked flag plus live direct blocks/waits-for/conditional-blocks edges),
// but with its OWN sentinel — ErrClaimBlocked, never ErrCloseBlocked — carried
// on a typed BlockedError whose blocker list keeps the edge's dependency kind.
func TestEnforceClaimPolicyInTxRefusesWithClaimBlockedSentinel(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const id, blocker = "claim-guard-target", "claim-guard-blocker"

	mock.ExpectQuery(`SELECT is_blocked FROM issues WHERE id = \?`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"is_blocked"}).AddRow(1))
	// The edge lives in ONE dependency table, as a real blocks edge does: the
	// durable dependencies table holds it and the wisp table's query answers
	// empty.
	mock.ExpectQuery(`(?s)SELECT\s+.*AS\s+depends_on_id,\s*type\s+FROM\s+dependencies\s+WHERE\s+issue_id\s*=\s*\?\s+AND\s+type\s+IN`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"depends_on_id", "type"}).AddRow(blocker, "blocks"))
	mock.ExpectQuery(`(?s)SELECT\s+.*AS\s+depends_on_id,\s*type\s+FROM\s+wisp_dependencies\s+WHERE\s+issue_id\s*=\s*\?\s+AND\s+type\s+IN`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"depends_on_id", "type"}))
	mock.ExpectQuery(`SELECT id, status FROM issues WHERE id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(blocker, types.StatusOpen))
	mock.ExpectQuery(`SELECT id, status FROM wisps WHERE id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}))

	err := EnforceClaimPolicyInTx(context.Background(), tx, id)
	if !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("EnforceClaimPolicyInTx = %v, want ErrClaimBlocked", err)
	}
	if errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("claim guard refused with the close sentinel: %v", err)
	}
	var blocked *publicops.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("refusal is not a *BlockedError: %T", err)
	}
	if blocked.IssueID != id || len(blocked.Blockers) != 1 ||
		blocked.Blockers[0].ID != blocker || blocked.Blockers[0].Type != types.DepBlocks {
		t.Fatalf("typed refusal = %#v, want issue %s blocked by one %s edge from %s",
			blocked, id, types.DepBlocks, blocker)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet claim-guard SQL expectations: %v", err)
	}
}

// TestEnforceClaimPolicyInTxPassesStaleFlagAndClearFlag pins the guard's
// fail-open half: a claim is refused only when the stored is_blocked flag AND
// a live direct blocker both say so, exactly like the close predicate. A stale
// flag (no live edges — a purely transitive block, or direct blockers that
// have since closed) must not refuse, and neither must a clear flag.
func TestEnforceClaimPolicyInTxPassesStaleFlagAndClearFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		blocked int
	}{
		{"stale flag without live blockers", 1},
		{"clear flag", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, mock, tx := beginMockTx(t)
			const id = "claim-guard-target"
			mock.ExpectQuery(`SELECT is_blocked FROM issues WHERE id = \?`).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"is_blocked"}).AddRow(tc.blocked))
			if tc.blocked != 0 {
				for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
					mock.ExpectQuery(`(?s)SELECT\s+.*AS\s+depends_on_id,\s*type\s+FROM\s+` + depTable + `\s+WHERE\s+issue_id\s*=\s*\?\s+AND\s+type\s+IN`).
						WithArgs(id).
						WillReturnRows(sqlmock.NewRows([]string{"depends_on_id", "type"}))
				}
			}
			if err := EnforceClaimPolicyInTx(context.Background(), tx, id); err != nil {
				t.Fatalf("EnforceClaimPolicyInTx = %v, want nil (no live direct blocker)", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet claim-guard SQL expectations: %v", err)
			}
		})
	}
}

// TestEnforceClaimPolicyInTxPropagatesReadFailure pins the guard's error
// policy: a read that fails is reported, never interpreted as permission.
func TestEnforceClaimPolicyInTxPropagatesReadFailure(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	readErr := errors.New("is_blocked read failed")
	mock.ExpectQuery(`SELECT is_blocked FROM issues WHERE id = \?`).
		WithArgs("claim-guard-target").
		WillReturnError(readErr)
	if err := EnforceClaimPolicyInTx(context.Background(), tx, "claim-guard-target"); !errors.Is(err, readErr) {
		t.Fatalf("EnforceClaimPolicyInTx = %v, want the read failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet claim-guard SQL expectations: %v", err)
	}
}
