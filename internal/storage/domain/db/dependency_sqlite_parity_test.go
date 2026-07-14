package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	storagesqlite "github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/storage/sqlitedialect"
	"github.com/steveyegge/beads/internal/types"
)

func TestDependencyUseCaseSQLiteRejectsHierarchyBlockingButAllowsSiblings(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "beads.db")

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if err := storagesqlite.InitSchema(ctx, raw); err != nil {
		_ = raw.Close()
		t.Fatalf("init sqlite schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	runner, err := sqlitedialect.Open(dbPath)
	if err != nil {
		t.Fatalf("open translated sqlite: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	issueRepo := NewIssueSQLRepository(runner)
	for _, id := range []string{"parent", "child", "sibling"} {
		if err := issueRepo.Insert(ctx, newTestIssue(id, id), "tester", domain.InsertIssueOpts{}); err != nil {
			t.Fatalf("insert issue %s: %v", id, err)
		}
	}

	depRepo := NewDependencySQLRepository(runner)
	for _, childID := range []string{"child", "sibling"} {
		if err := depRepo.Insert(ctx,
			newDep(childID, "parent", types.DepParentChild),
			"tester", domain.DepInsertOpts{}); err != nil {
			t.Fatalf("insert parent-child %s -> parent: %v", childID, err)
		}
	}

	uc := domain.NewDependencyUseCase(depRepo)
	if _, err := uc.AddDependencies(ctx, []*types.Dependency{
		newDep("sibling", "child", types.DepBlocks),
	}, "tester", domain.BulkAddDepsOpts{}); err != nil {
		t.Fatalf("siblings may carry ordering edges: %v", err)
	}

	tests := []struct {
		name      string
		dep       *types.Dependency
		wantError string
	}{
		{
			name:      "child blocked by ancestor",
			dep:       newDep("child", "parent", types.DepBlocks),
			wantError: "cannot be blocked by its ancestor",
		},
		{
			name:      "conditional child blocked by ancestor",
			dep:       newDep("child", "parent", types.DepConditionalBlocks),
			wantError: "cannot be blocked by its ancestor",
		},
		{
			name:      "parent blocked by descendant",
			dep:       newDep("parent", "child", types.DepBlocks),
			wantError: "cannot be blocked by its descendant",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.AddDependencies(ctx, []*types.Dependency{tt.dep}, "tester", domain.BulkAddDepsOpts{})
			if err == nil {
				t.Fatalf("AddDependencies(%s -> %s) succeeded, want hierarchy rejection", tt.dep.IssueID, tt.dep.DependsOnID)
			}
			if got := err.Error(); !strings.Contains(got, tt.wantError) {
				t.Fatalf("error = %q, want substring %q", got, tt.wantError)
			}
		})
	}
}

func TestDependencyUseCaseSQLiteRejectsCombinedSchedulingCycles(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "beads.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if err := storagesqlite.InitSchema(ctx, raw); err != nil {
		_ = raw.Close()
		t.Fatalf("init sqlite schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	runner, err := sqlitedialect.Open(dbPath)
	if err != nil {
		t.Fatalf("open translated sqlite: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	issueRepo := NewIssueSQLRepository(runner)
	for _, id := range []string{
		"block-a", "block-b", "block-c",
		"parent-a", "parent-b", "parent-c",
		"bulk-a", "bulk-b", "bulk-c",
		"deferred-a", "deferred-b", "deferred-c",
	} {
		if err := issueRepo.Insert(ctx, newTestIssue(id, id), "tester", domain.InsertIssueOpts{}); err != nil {
			t.Fatalf("insert issue %s: %v", id, err)
		}
	}
	uc := domain.NewDependencyUseCase(NewDependencySQLRepository(runner))

	for _, dep := range []*types.Dependency{
		newDep("block-a", "block-b", types.DepBlocks),
		newDep("block-b", "block-c", types.DepParentChild),
	} {
		if err := uc.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("seed block-closing path: %v", err)
		}
	}
	if err := uc.AddDependency(ctx, newDep("block-c", "block-a", types.DepConditionalBlocks), "tester"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("blocking closer error = %v, want combined-cycle rejection", err)
	}

	for _, dep := range []*types.Dependency{
		newDep("parent-a", "parent-b", types.DepBlocks),
		newDep("parent-b", "parent-c", types.DepConditionalBlocks),
	} {
		if err := uc.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("seed parent-child-closing path: %v", err)
		}
	}
	if err := uc.AddDependency(ctx, newDep("parent-c", "parent-a", types.DepParentChild), "tester"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("parent-child closer error = %v, want combined-cycle rejection", err)
	}

	for _, tc := range []struct {
		name string
		ids  [3]string
		opts domain.BulkAddDepsOpts
	}{
		{name: "per edge", ids: [3]string{"bulk-a", "bulk-b", "bulk-c"}},
		{name: "deferred whole graph", ids: [3]string{"deferred-a", "deferred-b", "deferred-c"}, opts: domain.BulkAddDepsOpts{SkipPerEdgeCycleCheck: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.AddDependencies(ctx, []*types.Dependency{
				newDep(tc.ids[0], tc.ids[1], types.DepBlocks),
				newDep(tc.ids[1], tc.ids[2], types.DepParentChild),
				newDep(tc.ids[2], tc.ids[0], types.DepBlocks),
			}, "tester", tc.opts)
			if err == nil || !strings.Contains(err.Error(), "cycle") {
				t.Fatalf("bulk mixed cycle error = %v, want rejection", err)
			}
		})
	}
}

func TestDependencyUseCaseSQLiteValidatesPlannedHierarchyBeforeBlocking(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "beads.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if err := storagesqlite.InitSchema(ctx, raw); err != nil {
		_ = raw.Close()
		t.Fatalf("init sqlite schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	runner, err := sqlitedialect.Open(dbPath)
	if err != nil {
		t.Fatalf("open translated sqlite: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	issueRepo := NewIssueSQLRepository(runner)
	for _, id := range []string{
		"anc-grand", "anc-parent", "anc-child",
		"desc-grand", "desc-parent", "desc-child",
		"pair-parent", "pair-child",
		"sibs-parent", "sibs-a", "sibs-b",
		"prec-parent", "prec-middle", "prec-child",
	} {
		if err := issueRepo.Insert(ctx, newTestIssue(id, id), "tester", domain.InsertIssueOpts{}); err != nil {
			t.Fatalf("insert issue %s: %v", id, err)
		}
	}

	depRepo := NewDependencySQLRepository(runner)
	uc := domain.NewDependencyUseCase(depRepo)
	tests := []struct {
		name      string
		deps      []*types.Dependency
		wantError string
	}{
		{
			name: "block-first child to planned ancestor",
			deps: []*types.Dependency{
				newDep("anc-child", "anc-grand", types.DepConditionalBlocks),
				newDep("anc-child", "anc-parent", types.DepParentChild),
				newDep("anc-parent", "anc-grand", types.DepParentChild),
			},
			wantError: "cannot be blocked by its ancestor",
		},
		{
			name: "block-first ancestor to planned descendant",
			deps: []*types.Dependency{
				newDep("desc-grand", "desc-child", types.DepBlocks),
				newDep("desc-child", "desc-parent", types.DepParentChild),
				newDep("desc-parent", "desc-grand", types.DepParentChild),
			},
			wantError: "cannot be blocked by its descendant",
		},
		{
			name: "block-first same pair conflict reports hierarchy",
			deps: []*types.Dependency{
				newDep("pair-child", "pair-parent", types.DepBlocks),
				newDep("pair-child", "pair-parent", types.DepParentChild),
			},
			wantError: "cannot be blocked by its ancestor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.AddDependencies(ctx, tt.deps, "tester", domain.BulkAddDepsOpts{})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
		})
	}
	_, err = uc.AddDependencies(ctx, []*types.Dependency{
		newDep("sibs-a", "sibs-b", types.DepBlocks), // Deliberately first.
		newDep("sibs-a", "sibs-parent", types.DepParentChild),
		newDep("sibs-b", "sibs-parent", types.DepParentChild),
	}, "tester", domain.BulkAddDepsOpts{})
	if err != nil {
		t.Fatalf("planned sibling ordering edge: %v", err)
	}

	for _, id := range []string{"wplan-grand", "wplan-parent", "wplan-child"} {
		issue := newTestIssue(id, id)
		issue.Ephemeral = true
		if err := issueRepo.Insert(ctx, issue, "tester", domain.InsertIssueOpts{UseWispsTable: true}); err != nil {
			t.Fatalf("insert wisp %s: %v", id, err)
		}
	}
	_, err = uc.AddWispDependencies(ctx, []*types.Dependency{
		newDep("wplan-child", "wplan-grand", types.DepConditionalBlocks), // Deliberately first.
		newDep("wplan-child", "wplan-parent", types.DepParentChild),
		newDep("wplan-parent", "wplan-grand", types.DepParentChild),
	}, "tester", domain.BulkAddDepsOpts{})
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("planned wisp hierarchy error = %v, want ancestor rejection", err)
	}

	// If a legacy blocking path also forms a cycle, hierarchy remains the
	// canonical error, matching the classic issueops path.
	for _, dep := range []*types.Dependency{
		newDep("prec-child", "prec-parent", types.DepParentChild),
		newDep("prec-parent", "prec-middle", types.DepBlocks),
		newDep("prec-middle", "prec-child", types.DepBlocks),
	} {
		if err := depRepo.Insert(ctx, dep, "tester", domain.DepInsertOpts{CycleValidated: true}); err != nil {
			t.Fatalf("seed dependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	err = uc.AddDependency(ctx, newDep("prec-child", "prec-parent", types.DepBlocks), "tester")
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("combined hierarchy/cycle error = %v, want ancestor rejection", err)
	}
}
