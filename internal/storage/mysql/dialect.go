package mysql

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/mysqldialect"
)

// mysqlDialect is the MySQL SQL flavor. bd's canonical SQL is already MySQL, so this
// is nearly the identity dialect: mysqldialect.Open wraps go-sql-driver with a single
// rewrite (the self-referential is_blocked recompute UPDATE that MySQL 8 rejects) and
// passes everything else through verbatim. The dsn already targets the workspace
// database.
type mysqlDialect struct {
	dsn string
}

func (d mysqlDialect) Name() string { return "mysql" }

func (d mysqlDialect) Open(_ context.Context) (*sql.DB, error) {
	return mysqldialect.Open(d.dsn)
}
