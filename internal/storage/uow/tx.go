package uow

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/domain/db"
)

type Tx interface {
	Runner() db.Runner
	Commit(ctx context.Context, message string) error
	Rollback(ctx context.Context) error
	RollbackUnlessCommitted(ctx context.Context)
}

type TxProvider interface {
	BeginTx(ctx context.Context) (Tx, error)
}
