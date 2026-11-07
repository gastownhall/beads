package uow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// conflictOnceProvider hands out units of work whose FIRST Commit runs
// beforeFail and then refuses with a serialization failure without
// committing, so RunTxResult rolls that attempt back and replays it: the
// commit-time conflict a concurrent writer causes.
type conflictOnceProvider struct {
	UnitOfWorkProvider
	beforeFail func(ctx context.Context) error
	failed     atomic.Int32
}

func (p *conflictOnceProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &conflictOnceUOW{UnitOfWork: uw, provider: p}, nil
}

type conflictOnceUOW struct {
	UnitOfWork
	provider *conflictOnceProvider
}

func (u *conflictOnceUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

func (u *conflictOnceUOW) Commit(ctx context.Context, message string) error {
	if u.provider.failed.CompareAndSwap(0, 1) {
		if err := u.provider.beforeFail(ctx); err != nil {
			return err
		}
		return newMySQLError(1213)
	}
	return u.UnitOfWork.Commit(ctx, message)
}

// TestImportBatchReplayRestartsFromTheRequest: an import row without an ID is
// minted one inside the transaction, and the create path writes that ID (and
// its timestamps) back into the caller's issue. When the attempt loses a
// commit-time conflict, the replay must start from the request as the caller
// made it. Reusing the rolled-back attempt's ID would, in counter mode, upsert
// over the issue the concurrent writer committed under that number.
func TestImportBatchReplayRestartsFromTheRequest(t *testing.T) {
	ctx := context.Background()
	inner := newUOWRoleFixtureProvider(t, ctx, "imr")
	if err := RunTx(ctx, inner, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: counter mode", uw.ConfigUseCase().SetConfig(ctx, "issue_id_mode", "counter")
	}); err != nil {
		t.Fatalf("set counter mode: %v", err)
	}

	provider := &conflictOnceProvider{UnitOfWorkProvider: inner}
	provider.beforeFail = func(ctx context.Context) error {
		// The competitor commits imr-1 while the import's first attempt,
		// which minted imr-1 too, is still open.
		return RunTx(ctx, inner, func(ctx context.Context, uw UnitOfWork) (string, error) {
			competitor := &types.Issue{ID: "imr-1", Title: "competitor", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2}
			_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: competitor, ExplicitID: competitor.ID, CreateOnly: true}, "competitor")
			return "bd: competitor create", err
		})
	}
	imp, err := NewImporter(provider)
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}

	row := &types.Issue{Title: "imported row", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2}
	result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{Actor: "importer", Issues: []*types.Issue{row}})
	if err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}
	if provider.failed.Load() != 1 {
		t.Fatal("the first attempt never reached its commit: no replay happened")
	}
	if result.Created != 1 {
		t.Fatalf("Created = %d (stale-rejected %v), want 1: the replay must create the row afresh", result.Created, result.StaleRejectedIDs)
	}
	if row.ID == "" || row.ID == "imr-1" {
		t.Fatalf("imported row id = %q, want a fresh id: the replay re-used the rolled-back attempt's imr-1", row.ID)
	}

	read := func(id string) *types.Issue {
		t.Helper()
		issue, err := RunTxRead(ctx, inner, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
			return uw.IssueUseCase().GetIssue(ctx, id)
		})
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return issue
	}
	if got := read("imr-1"); got.Title != "competitor" {
		t.Fatalf("imr-1 holds %q, want the competitor's issue to survive", got.Title)
	}
	if got := read(row.ID); got.Title != "imported row" {
		t.Fatalf("%s holds %q, want the imported row", row.ID, got.Title)
	}
}
