package uow

import (
	"context"
	"database/sql"
	"fmt"
)

type MaintenanceProvider interface {
	RunNonTx(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error
}

var _ MaintenanceProvider = (*doltSQLProvider)(nil)

func (p *doltSQLProvider) RunNonTx(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("uow: pin connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return fn(ctx, conn)
}
