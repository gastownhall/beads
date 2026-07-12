package sqlite

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/sqlitedialect"
)

// sqliteDialect is the SQLite SQL flavor: it opens a *sql.DB over modernc.org/sqlite
// whose driver translates bd's canonical MySQL SQL to SQLite (small: mainly
// INSERT IGNORE → INSERT OR IGNORE). The dsn already carries the file path + pragmas.
type sqliteDialect struct{ dsn string }

func (d sqliteDialect) Name() string { return "sqlite" }

func (d sqliteDialect) Open(_ context.Context) (*sql.DB, error) {
	db, err := sqlitedialect.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	// SQLite is single-writer. Pin the pool to one connection so overlapping
	// operations from parallel workers sharing this *Store serialize in-process
	// (waiting on the connection) instead of colliding with a raw SQLITE_BUSY /
	// "database is locked" — the same single-connection posture the Dolt backend
	// takes (SetMaxOpenConns(1)). Cross-process contention is covered by the DSN's
	// busy_timeout + WAL.
	db.SetMaxOpenConns(1)
	return db, nil
}
