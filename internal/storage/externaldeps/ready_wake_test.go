package externaldeps

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

type wakeStore struct {
	*fakeStore
	wake func()
}

func (s *wakeStore) WakeExpiredDefersAdvisory(context.Context) { s.wake() }

func (s *wakeStore) SearchIssuesWithCounts(ctx context.Context, _ string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	rows := make([]*types.IssueWithCounts, 0, len(filter.IDs))
	for _, id := range filter.IDs {
		row, err := s.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, &types.IssueWithCounts{Issue: row})
	}
	return rows, nil
}

func TestReadyClaimerWakesBeforeExternalFilteredSelection(t *testing.T) {
	for _, sweepSucceeds := range []bool{true, false} {
		name := "wake succeeds"
		if !sweepSucceeds {
			name = "failed advisory wake still allows claim"
		}
		t.Run(name, func(t *testing.T) {
			blocked, eligible := issue("be-blocked"), issue("be-eligible")
			raw := &fakeStore{deps: map[string][]*types.Dependency{
				blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
			}}
			if !sweepSucceeds {
				raw.ready = []*types.Issue{blocked, eligible}
			}
			wakes := 0
			backend := &wakeStore{fakeStore: raw, wake: func() {
				wakes++
				if sweepSucceeds {
					raw.ready = []*types.Issue{blocked, eligible}
				}
			}}
			// An intervening decorator must not hide the wake capability.
			inner := storage.NewHookFiringStore(backend, nil)
			claimer, err := New(inner, nil, nil).ReadyClaimer()
			if err != nil {
				t.Fatal(err)
			}
			got, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			if wakes != 1 || got.Claimed == nil || got.Claimed.ID != eligible.ID {
				t.Fatalf("wakes=%d, claimed=%v; want one wake and %s", wakes, got.Claimed, eligible.ID)
			}
		})
	}
}
