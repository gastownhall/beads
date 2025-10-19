package sqlite

import (
	"context"
	"database/sql"
)

// QueryContext exposes the underlying database QueryContext method for advanced queries
// This is used by commands that need direct SQL access (e.g., bd stale)
func (s *SQLiteStorage) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

// BeginTx starts a new database transaction
// This is used by commands that need to perform multiple operations atomically
func (s *SQLiteStorage) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}
