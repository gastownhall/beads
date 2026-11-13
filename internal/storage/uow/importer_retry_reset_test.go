package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// retryOnceProvider fails the FIRST attempt's commit with a retryable
// serialization error (MySQL 1213) so RunTxResult replays the import closure,
// and runs onRetry right before the second attempt opens its unit of work.
type retryOnceProvider struct {
	UnitOfWorkProvider
	calls   int
	onRetry func(ctx context.Context)
}

func (p *retryOnceProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	p.calls++
	if p.calls == 2 && p.onRetry != nil {
		p.onRetry(ctx)
	}
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &failFirstCommitUOW{UnitOfWork: uw, fail: p.calls == 1}, nil
}

type failFirstCommitUOW struct {
	UnitOfWork
	fail bool
}

func (u *failFirstCommitUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

func (u *failFirstCommitUOW) Commit(ctx context.Context, message string) error {
	if u.fail {
		return newMySQLError(1213)
	}
	return u.UnitOfWork.Commit(ctx, message)
}

// TestImporterRetryReplaysWithCallerIDsReset (bd-8qq0ho, GH#8455): a retried
// ImportBatch must replay with the caller's structs as they were before the
// first attempt. An issue submitted without an ID must be re-minted on the
// retry, not upserted under the ID the rolled-back attempt wrote onto the
// caller's struct — otherwise a row another writer created under that ID in
// between is overwritten (or the import is stale-rejected) instead of the
// import landing under a fresh ID.
func TestImporterRetryReplaysWithCallerIDsReset(t *testing.T) {
	ctx := context.Background()
	inner := newUOWRoleFixtureProvider(t, ctx, "rty")
	kit := newUOWRoleFixtureKit(inner, "rty")

	issue := &types.Issue{
		Title: "Imported without an id", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 2,
	}
	var idAtRetry string
	provider := &retryOnceProvider{UnitOfWorkProvider: inner}
	provider.onRetry = func(ctx context.Context) {
		idAtRetry = issue.ID
		if idAtRetry == "" {
			return
		}
		// Another writer lands a row under the id the rolled-back attempt
		// minted before the retry runs.
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: idAtRetry, Title: "concurrent writer row", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2,
		}, "other-writer"); err != nil {
			t.Fatalf("seed concurrent row %q: %v", idAtRetry, err)
		}
	}

	imp, err := NewImporter(provider)
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
		Actor:  "importer-test",
		Source: "retry-test",
		Issues: []*types.Issue{issue},
	})
	if err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}
	if provider.calls < 2 {
		t.Fatalf("import ran %d attempt(s); the test needs a retry", provider.calls)
	}

	if idAtRetry == "" {
		t.Fatal("attempt 1 minted no id onto the caller's struct; the fixture cannot observe the hazard")
	}
	var title string
	if err := kit.QueryScalar(ctx, "SELECT title FROM issues WHERE id = ?", []any{idAtRetry}, &title); err != nil {
		t.Fatalf("read concurrent row: %v", err)
	}
	// The reset runs at the top of the replayed closure (after the retry's
	// NewUOW, which is where idAtRetry was read), so the assertion is on the
	// BEHAVIOR it exists to produce: the committed attempt's id on the
	// caller's struct must be a fresh re-mint, not the aborted attempt's
	// mint the concurrent writer now owns.
	if issue.ID == idAtRetry {
		t.Errorf("the import landed under the aborted attempt's mint %q (the caller's struct kept it across the retry "+
			"instead of being reset and re-minted); result=%+v, row %q now has title %q", idAtRetry, result, idAtRetry, title)
	}
	if result.Created != 1 || len(result.StaleRejectedIDs) != 0 {
		t.Errorf("import result = %+v, want Created=1 with nothing stale-rejected", result)
	}
	if title != "concurrent writer row" {
		t.Errorf("the concurrent writer's row at %s = %q, want its own title", idAtRetry, title)
	}
}
