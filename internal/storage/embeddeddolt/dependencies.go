//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.AddDependencyInTx(ctx, tx, dep, actor, issueops.AddDependencyOpts{
			IsCrossPrefix: types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID),
		})
	})
}
