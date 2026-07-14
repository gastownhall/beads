package pgdialect

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Open returns a *sql.DB over pgx whose Prepare/PrepareContext translate bd's
// canonical (MySQL-dialect) SQL to Postgres via Translate. searchPath pins the
// workspace schema.
//
// This wrapper deliberately exposes no Queryer/Execer fast path, so database/sql
// routes every statement through Prepare — translation therefore happens in
// exactly one place, and an untranslatable construct fails loudly at PREPARE.
func Open(dsn, searchPath string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx's ParseConfigError leaves ?password= URL query params cleartext; scrub
		// before the error can reach any log.
		return nil, ScrubDSNError(dsn, err)
	}
	if searchPath != "" {
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = map[string]string{}
		}
		cfg.RuntimeParams["search_path"] = searchPath
	}
	return sql.OpenDB(&connector{base: stdlib.GetConnector(*cfg)}), nil
}

// OpenRaw returns a *sql.DB over pgx WITHOUT the translating connector wrapper,
// so SQL text reaches Postgres verbatim. It exists for schema DDL: the DDL is
// already native Postgres, and routing it through Translate mangles the
// $$-quoted function bodies and rewrites DDL constructs (e.g. now()). searchPath
// pins the workspace schema exactly as Open does. Callers must use native $n
// placeholders, not ?, since Translate is bypassed.
func OpenRaw(dsn, searchPath string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// See Open: pgx does not redact query-param passwords in parse errors.
		return nil, ScrubDSNError(dsn, err)
	}
	if searchPath != "" {
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = map[string]string{}
		}
		cfg.RuntimeParams["search_path"] = searchPath
	}
	return sql.OpenDB(stdlib.GetConnector(*cfg)), nil
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

// conn wraps a pgx driver.Conn and translates SQL text at prepare time.
type conn struct{ base driver.Conn }

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	q, err := Translate(query)
	if err != nil {
		return nil, err
	}
	base, err := c.base.Prepare(q)
	if err != nil {
		return nil, err
	}
	return &stmt{base: base, conn: c.base}, nil
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	q, err := Translate(query)
	if err != nil {
		return nil, err
	}
	var base driver.Stmt
	if p, ok := c.base.(driver.ConnPrepareContext); ok {
		base, err = p.PrepareContext(ctx, q)
	} else {
		base, err = c.base.Prepare(q)
	}
	if err != nil {
		return nil, err
	}
	return &stmt{base: base, conn: c.base}, nil
}

func (c *conn) Close() error { return c.base.Close() }

//nolint:staticcheck // SA1019: driver.Conn requires the deprecated Begin; BeginTx is preferred and forwarded below.
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

// stmt wraps a pgx driver.Stmt so bd's MySQL TINYINT(1) discipline survives on
// Postgres: bd binds Go bool arguments to columns that are smallint here (not
// boolean), so CheckNamedValue coerces bool -> 0/1 before binding. Every other
// argument type is forwarded to pgx's own NamedValueChecker so pgx's rich type
// handling (arrays, time, etc.) is preserved.
type stmt struct {
	base driver.Stmt
	conn driver.Conn
}

func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if b, ok := nv.Value.(bool); ok {
		nv.Value = boolToInt64(b)
		return nil
	}
	if chk, ok := s.conn.(driver.NamedValueChecker); ok {
		return chk.CheckNamedValue(nv)
	}
	return driver.ErrSkip // fall back to database/sql's default conversion
}

func (s *stmt) Close() error  { return s.base.Close() }
func (s *stmt) NumInput() int { return s.base.NumInput() }

//nolint:staticcheck // driver.Stmt requires the deprecated Exec; ExecContext is preferred and forwarded below.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) { return s.base.Exec(args) }

//nolint:staticcheck // driver.Stmt requires the deprecated Query; QueryContext is preferred and forwarded below.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) { return s.base.Query(args) }

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := s.base.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return s.base.Exec(namedToValues(args)) //nolint:staticcheck
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := s.base.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	return s.base.Query(namedToValues(args)) //nolint:staticcheck
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func namedToValues(named []driver.NamedValue) []driver.Value {
	vals := make([]driver.Value, len(named))
	for i, n := range named {
		vals[i] = n.Value
	}
	return vals
}
