package uow

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedBlockedRecheckAfterRacingUnblocks is gastownhall/beads#6716 on
// the proxied (uow) write path, the one every proxied `bd close`,
// `bd update --status closed`, `bd dep remove`, `bd delete`, proxied batch
// close and bd serve reach.
//
// Two blockers A and B of one dependent C are taken away by two units of
// work whose transactions overlap: the first begins and writes while both
// blockers are open, the second then writes and commits, and the first
// commits last. Each in-transaction recompute saw the other blocker still in
// place, so neither wrote C, and without a post-commit recheck C stays
// is_blocked=1 and hidden from ready work on every committed snapshot.
func TestProxiedBlockedRecheckAfterRacingUnblocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const prefix = "rbr"
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	kit := newUOWRoleFixtureKit(provider, prefix)

	closeIssue := func(id string) func(context.Context, UnitOfWork) error {
		return func(ctx context.Context, uw UnitOfWork) error {
			_, err := uw.IssueUseCase().CloseIssue(ctx, id, domain.CloseIssueParams{Reason: "done"}, "racer")
			return err
		}
	}
	closeChecked := func(id string) func(context.Context, UnitOfWork) error {
		return func(ctx context.Context, uw UnitOfWork) error {
			_, err := uw.IssueUseCase().CloseIssueChecked(ctx, id, domain.CloseIssueParams{Reason: "done"}, "racer", false)
			return err
		}
	}
	updateClosed := func(id string) func(context.Context, UnitOfWork) error {
		return func(ctx context.Context, uw UnitOfWork) error {
			return uw.IssueUseCase().UpdateIssue(ctx, id, map[string]any{"status": string(types.StatusClosed)}, "racer")
		}
	}
	closeWisp := func(id string) func(context.Context, UnitOfWork) error {
		return func(ctx context.Context, uw UnitOfWork) error {
			_, err := uw.IssueUseCase().CloseWisp(ctx, id, domain.CloseIssueParams{Reason: "done"}, "racer")
			return err
		}
	}

	cases := []struct {
		name string
		// wispBlockers seeds A and B as wisps instead of issues.
		wispBlockers bool
		// first and second take blocker A and blocker B away; first commits last.
		first, second func(a, b, c string) func(context.Context, UnitOfWork) error
	}{
		{
			name:   "close vs close",
			first:  func(a, _, _ string) func(context.Context, UnitOfWork) error { return closeIssue(a) },
			second: func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeIssue(b) },
		},
		{
			// The batch closer's per-item close (bd close a b, proxied).
			name:   "checked close vs checked close",
			first:  func(a, _, _ string) func(context.Context, UnitOfWork) error { return closeChecked(a) },
			second: func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeChecked(b) },
		},
		{
			// bd update --status closed, proxied.
			name:   "update to closed vs close",
			first:  func(a, _, _ string) func(context.Context, UnitOfWork) error { return updateClosed(a) },
			second: func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeIssue(b) },
		},
		{
			name: "dependency removal vs close",
			first: func(a, _, c string) func(context.Context, UnitOfWork) error {
				return func(ctx context.Context, uw UnitOfWork) error {
					return uw.DependencyUseCase().RemoveDependency(ctx, c, a, "racer")
				}
			},
			second: func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeIssue(b) },
		},
		{
			name: "delete vs close",
			first: func(a, _, _ string) func(context.Context, UnitOfWork) error {
				return func(ctx context.Context, uw UnitOfWork) error {
					_, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{IDs: []string{a}, EnforceCascadePolicy: true, Force: true}, "racer")
					return err
				}
			},
			second: func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeIssue(b) },
		},
		{
			name:         "wisp close vs wisp close",
			wispBlockers: true,
			first:        func(a, _, _ string) func(context.Context, UnitOfWork) error { return closeWisp(a) },
			second:       func(_, b, _ string) func(context.Context, UnitOfWork) error { return closeWisp(b) },
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := string(rune('a' + i))
			a, b, c := prefix+"-"+n+"blka", prefix+"-"+n+"blkb", prefix+"-"+n+"dep"
			seed := kit.CreateIssue
			if tc.wispBlockers {
				seed = kit.CreateWisp
			}
			for _, id := range []string{a, b} {
				if err := seed(ctx, &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}, "seeder"); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}
			if err := kit.CreateIssue(ctx, &types.Issue{ID: c, Title: c, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}, "seeder"); err != nil {
				t.Fatalf("seed %s: %v", c, err)
			}
			for _, blocker := range []string{a, b} {
				if err := kit.AddDependency(ctx, &types.Dependency{IssueID: c, DependsOnID: blocker, Type: types.DepBlocks}, "seeder"); err != nil {
					t.Fatalf("add %s -> %s: %v", c, blocker, err)
				}
			}
			requireBlocked(t, ctx, kit, c, true)

			first, err := provider.NewUOW(ctx)
			if err != nil {
				t.Fatalf("open first unit of work: %v", err)
			}
			defer first.Close(ctx)
			if err := tc.first(a, b, c)(ctx, first); err != nil {
				t.Fatalf("first write: %v", err)
			}

			if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "second racing write", tc.second(a, b, c)(ctx, uw)
			}); err != nil {
				t.Fatalf("second write: %v", err)
			}
			// The second write's own recheck ran while the first was still
			// uncommitted, so the dependent is legitimately still blocked.
			requireBlocked(t, ctx, kit, c, true)

			if err := first.Commit(ctx, "first racing write"); err != nil {
				t.Fatalf("commit first write: %v", err)
			}

			requireBlocked(t, ctx, kit, c, false)
			ready, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (domain.SearchPage, error) {
				return uw.IssueUseCase().GetReadyWork(ctx, types.WorkFilter{})
			})
			if err != nil {
				t.Fatalf("ready work: %v", err)
			}
			if !slices.ContainsFunc(ready.Items, func(i *types.Issue) bool { return i.ID == c }) {
				t.Fatalf("%s has no open blockers but is missing from ready work", c)
			}
		})
	}
}

func requireBlocked(t *testing.T, ctx context.Context, kit roleFixtureKit, id string, want bool) {
	t.Helper()
	var blocked bool
	if err := kit.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{id}, &blocked); err != nil {
		t.Fatalf("read is_blocked of %s: %v", id, err)
	}
	if blocked != want {
		t.Fatalf("%s is_blocked = %v, want %v", id, blocked, want)
	}
}
