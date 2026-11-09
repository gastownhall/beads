package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The independent SQL repository reached through UOW must reclassify a
// pre-fix external-column row on a same-type re-add, not return idempotently
// before the column and derived is_blocked are repaired.
func TestCrossPrefixLegacyExternalReAddViaUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "a")
	kit := newUOWRoleFixtureKit(provider, "a")
	if err := kit.SetConfig(ctx, "allowed_prefixes", "a,b"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-uow-legacy-dependent", "b-uow-legacy-blocker"} {
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		_, err := uw.RawSQLUseCase().Exec(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata) VALUES (?, ?, ?, 'blocks', UTC_TIMESTAMP(), 'tester', '{}')",
			"legacy-uow-8300", "a-uow-legacy-dependent", "b-uow-legacy-blocker")
		return "seed pre-fix external-column edge", err
	}); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	dep := &types.Dependency{IssueID: "a-uow-legacy-dependent", DependsOnID: "b-uow-legacy-blocker", Type: types.DepBlocks}
	for i := 0; i < 2; i++ {
		if err := kit.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("re-add %d: %v", i+1, err)
		}
	}
	var rowID, local, external string
	if err := kit.QueryScalar(ctx,
		"SELECT id, COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_external, '') FROM dependencies WHERE issue_id = ?",
		[]any{dep.IssueID}, &rowID, &local, &external); err != nil {
		t.Fatal(err)
	}
	if rowID != "legacy-uow-8300" || local != dep.DependsOnID || external != "" {
		t.Fatalf("UOW legacy re-add id=%q local=%q external=%q", rowID, local, external)
	}
	var blocked int
	if err := kit.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{dep.IssueID}, &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 1 {
		t.Fatalf("UOW legacy re-add left is_blocked=%d, want 1", blocked)
	}
}
