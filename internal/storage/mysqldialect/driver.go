// Package mysqldialect is the MySQL backend's thin driver wrapper. bd's canonical SQL
// is MySQL, so unlike pgdialect there is no translation — the ONE rewrite is the
// self-referential is_blocked recompute UPDATE, which MySQL 8 rejects (error 1093: a
// table may not be updated while referenced in its own subquery) but Dolt and Postgres
// permit. The rewrite is isolated here, so the shared issueops/sqlkit layers and the
// Dolt/Postgres backends are entirely untouched.
//
// A portable, perf-reviewed rewrite in shared issueops is tracked as a backlog item;
// this wrapper is the per-backend containment chosen to avoid touching Dolt's hot path.
package mysqldialect

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/go-sql-driver/mysql"
)

// Open returns a *sql.DB over go-sql-driver whose Prepare rewrites the recompute
// UPDATE (see rewriteSelfRefUpdate); every other statement passes through verbatim.
// Like pgdialect, the wrapper exposes no Execer/Queryer fast path, so database/sql
// routes every statement through Prepare — the one place the rewrite happens.
func Open(dsn string) (*sql.DB, error) {
	base, err := (mysql.MySQLDriver{}).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(&connector{base: base}), nil
}

type connector struct{ base driver.Connector }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	cn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &conn{base: cn}, nil
}

func (c *connector) Driver() driver.Driver { return c.base.Driver() }

// conn wraps the mysql driver.Conn and rewrites the recompute UPDATE at prepare time.
// It deliberately implements no Execer/Queryer so database/sql prepares every stmt.
type conn struct{ base driver.Conn }

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.base.Prepare(rewriteSelfRefUpdate(query))
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	q := rewriteSelfRefUpdate(query)
	if p, ok := c.base.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.base.Prepare(q)
}

func (c *conn) Close() error { return c.base.Close() }

//nolint:staticcheck // SA1019: driver.Conn requires the deprecated Begin; BeginTx forwarded below.
func (c *conn) Begin() (driver.Tx, error) { return c.base.Begin() }

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.base.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.base.Begin() //nolint:staticcheck
}

func (c *conn) ResetSession(ctx context.Context) error {
	if rs, ok := c.base.(driver.SessionResetter); ok {
		return rs.ResetSession(ctx)
	}
	return nil
}

func (c *conn) IsValid() bool {
	if v, ok := c.base.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *conn) Ping(ctx context.Context) error {
	if p, ok := c.base.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}
