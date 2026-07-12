package postgres

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// pgDialect is the Postgres SQL flavor: it opens a *sql.DB whose driver
// translates bd's canonical (MySQL-dialect) SQL to Postgres, and pins the
// workspace schema via search_path.
type pgDialect struct {
	dsn        string
	searchPath string
}

func (d pgDialect) Name() string { return "postgres" }

func (d pgDialect) Open(_ context.Context) (*sql.DB, error) {
	return pgdialect.Open(d.dsn, d.searchPath)
}
