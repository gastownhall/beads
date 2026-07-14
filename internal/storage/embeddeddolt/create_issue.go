//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}
	// Route infra types to wisps, matching DoltStore.CreateIssue behavior.
	if s.IsInfraTypeCtx(ctx, issue.IssueType) {
		issue.Ephemeral = true
	}

	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		// SkipPrefixValidation matches DoltStore.CreateIssue, which does not
		// validate prefixes for explicit IDs on the single-issue path.
		bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		})
		if err != nil {
			return err
		}
		return issueops.CreateIssueInTx(ctx, tx, bc, issue, actor)
	})
}

func (s *EmbeddedDoltStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: false,
	})
}

func (s *EmbeddedDoltStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}

	// An all-wisps batch marks every issue ephemeral (unless it is explicitly
	// no-history) so the rows route to the wisp tables; wisps skip Dolt versioning
	// either way. Every batch — all-wisps or mixed/durable — then runs in ONE
	// transaction through the batch issueops.CreateIssuesInTx.
	//
	// Do NOT loop the single-issue CreateIssueInTx for the all-wisps case: it never
	// runs the batch dependency-persist pass, so an all-wisps batch carrying inline
	// dependencies (e.g. importing no-history beads with inter-bead edges, which
	// export does include) would silently drop every edge — not even reported via
	// OnSkippedDependency — and one transaction per issue would forfeit batch
	// atomicity. This mirrors the server-mode DoltStore's all-wisps arm.
	if issueops.AllWisps(issues) {
		for _, issue := range issues {
			if !issue.NoHistory {
				issue.Ephemeral = true
			}
		}
	}

	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.CreateIssuesInTx(ctx, tx, issues, actor, opts)
	})
}
