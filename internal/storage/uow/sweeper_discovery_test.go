package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The sweeper's live-dependent protection reads edges and liveness in ROUNDS:
// one batch of incoming edges, one liveness scan of the sources it found, then
// the next batch for the sources that were not live. The failure mode these
// tests pin is a read that fails in a LATER round: the first round already
// told the sweep about a chain, and the question is whether it deletes on that
// partial knowledge or aborts (#7043). The fakes below are the scripted
// storage behind the mock unit-of-work seam: only what the sweeper reads, each
// answer served in call order.

// sweeperDiscoveryConfig is a workspace with no retained types and no custom
// statuses, so the sweep's config reads cannot be the failure under test.
type sweeperDiscoveryConfig struct{ domain.ConfigUseCase }

func (sweeperDiscoveryConfig) GetConfig(context.Context, string) (string, error) {
	return "", nil
}

func (sweeperDiscoveryConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

// sweeperDiscoveryIssues answers the candidate search and the liveness scans,
// and records whether the sweep reached the delete on the answers it gave.
type sweeperDiscoveryIssues struct {
	domain.IssueUseCase
	candidates []*types.Issue
	liveIDs    map[string]bool
	deletes    int
}

func (f *sweeperDiscoveryIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	// The liveness scan names the ids it asks about; the candidate search
	// does not. The sweeper relies on exactly that difference.
	if len(filter.IDs) > 0 {
		items := make([]*types.Issue, 0, len(filter.IDs))
		for _, id := range filter.IDs {
			if f.liveIDs[id] {
				items = append(items, &types.Issue{ID: id})
			}
		}
		return domain.SearchPage{Items: items}, nil
	}
	return domain.SearchPage{Items: f.candidates}, nil
}

func (f *sweeperDiscoveryIssues) DeleteIssues(context.Context, domain.DeleteIssuesParams, string) (domain.DeleteIssuesResult, error) {
	f.deletes++
	return domain.DeleteIssuesResult{}, nil
}

// sweeperDiscoveryDeps answers incoming-edge reads in call order and fails on
// the configured call, so a test can place the failure in any round.
type sweeperDiscoveryDeps struct {
	domain.DependencyUseCase
	// incoming holds one answer per call; calls beyond it read no edges.
	incoming []map[string][]*types.Dependency
	failOn   int
	calls    int
}

func (f *sweeperDiscoveryDeps) ListByIssueIDs(context.Context, []string, domain.DepListFilter) (domain.DepBulkResult, error) {
	f.calls++
	if f.calls == f.failOn {
		return domain.DepBulkResult{}, errors.New("dependency read failed")
	}
	if f.calls <= len(f.incoming) {
		return domain.DepBulkResult{Incoming: f.incoming[f.calls-1]}, nil
	}
	return domain.DepBulkResult{}, nil
}

func (f *sweeperDiscoveryDeps) ListByWispIDs(context.Context, []string, domain.DepListFilter) (domain.DepBulkResult, error) {
	return domain.DepBulkResult{}, nil
}

// TestSweeperAbortsWhenADiscoveryReadFails pins the abort half of the
// protection contract: a candidate sits under a closed middle the cutoff held
// out of the selection, the first edge read finds the middle, and the read of
// the MIDDLE's incoming edges — the one that would have found the live leaf —
// fails. The sweep must return the error and delete nothing rather than treat
// what it managed to read as the whole answer.
func TestSweeperAbortsWhenADiscoveryReadFails(t *testing.T) {
	closedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cutoff := closedAt.AddDate(0, 1, 0)
	root := &types.Issue{
		ID:       "bd-err-root",
		Title:    "root",
		Status:   types.StatusClosed,
		Priority: 2,
		ClosedAt: &closedAt,
	}
	issues := &sweeperDiscoveryIssues{candidates: []*types.Issue{root}}
	deps := &sweeperDiscoveryDeps{
		incoming: []map[string][]*types.Dependency{
			// Round 1: the candidate's own incoming edge, from a closed
			// middle the cutoff held out of the candidate set.
			{"bd-err-root": {{IssueID: "bd-err-middle", DependsOnID: "bd-err-root", Type: types.DepParentChild}}},
		},
		failOn: 2,
	}
	uw := &mockUnitOfWork{
		issueUseCase:      issues,
		configUseCase:     sweeperDiscoveryConfig{},
		dependencyUseCase: deps,
	}
	sweeper, err := NewSweeper(&mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}})
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}

	result, err := sweeper.Sweep(context.Background(), publicops.SweepRequest{
		Tier:                  publicops.SweepEphemeral,
		IDPattern:             "bd-err-*",
		ClosedBefore:          &cutoff,
		ProtectLiveDependents: true,
		DryRun:                true,
	})
	if err == nil {
		t.Fatalf("Sweep succeeded with Swept = %d; want the failed discovery read to abort it",
			result.Swept)
	}
	if issues.deletes != 0 {
		t.Errorf("DeleteIssues ran %d time(s); a sweep that could not finish reading the "+
			"protection graph must not delete", issues.deletes)
	}
}
