package sqlitedialect

import (
	"context"
	"database/sql"
	"database/sql/driver"

	sqlite "modernc.org/sqlite"
)

// Open returns a *sql.DB over modernc.org/sqlite whose Prepare translates bd's
// canonical MySQL SQL to SQLite (see Translate). Like pgdialect it exposes no
// Execer/Queryer fast path, so database/sql routes every statement through Prepare.
// The caller supplies foreign-key enforcement etc. via DSN pragmas.
func Open(dsn string) (*sql.DB, error) {
	return sql.OpenDB(&connector{dsn: dsn}), nil
}

// connector opens a fresh modernc connection per Connect. A zero-value
// modernc Driver carries no user-registered udfs/collations/hooks, which is exactly
// what the wedge wants; connection config travels in the DSN.
type connector struct{ dsn string }

func (c *connector) Connect(_ context.Context) (driver.Conn, error) {
	base, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &conn{base: base}, nil
}

func (c *connector) Driver() driver.Driver { return &sqlite.Driver{} }

type conn struct{ base driver.Conn }

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.base.Prepare(Translate(query))
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	q := Translate(query)
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
