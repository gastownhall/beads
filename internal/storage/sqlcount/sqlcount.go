// Package sqlcount wraps a database/sql/driver.Connector so a test or
// benchmark can count how many SQL statements a code path actually issues
// against a real driver, and optionally inject artificial per-statement
// latency to approximate a database that is not co-located with the caller.
//
// It exists for the large-batch-apply measurement work: issueops.ApplyBatchInTx
// takes a concrete *sql.Tx, not an interface, so the only seam available to
// count its real round trips is the database/sql/driver boundary itself. Wrapping the
// driver.Connector a backend already builds — mysql.NewConnector for the Dolt
// server backend, doltembed.NewConnector for the embedded backend — is the
// one construction point both backends share, so one wrapper measures both
// without touching either driver or any production code path.
package sqlcount

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"time"
)

// Counts tracks how many times each driver entry point that represents one
// SQL statement or round trip fired. The zero value is ready to use. Safe
// for concurrent use.
type Counts struct {
	prepare   int64
	exec      int64
	query     int64
	stmtExec  int64
	stmtQuery int64
	begin     int64
	commit    int64
	rollback  int64
}

// Snapshot is a point-in-time, plain copy of Counts, safe to diff, log or
// assert on without synchronization.
type Snapshot struct {
	Prepare   int64
	Exec      int64
	Query     int64
	StmtExec  int64
	StmtQuery int64
	Begin     int64
	Commit    int64
	Rollback  int64
}

// Total is every round trip this wrapper attributes to an executed SQL
// statement: a Prepare, a driver-level Exec/Query issued without a separate
// prepare, or an Exec/Query against an already-prepared statement. Begin,
// Commit and Rollback are reported on Snapshot but excluded here, because a
// caller measuring one transaction's BODY (issueops.ApplyBatchInTx, for
// instance) opens and closes that transaction outside the measured window.
func (s Snapshot) Total() int64 {
	return s.Prepare + s.Exec + s.Query + s.StmtExec + s.StmtQuery
}

// Sub returns the per-field difference s-prior, for isolating one call's
// contribution on a longer-lived connection.
func (s Snapshot) Sub(prior Snapshot) Snapshot {
	return Snapshot{
		Prepare:   s.Prepare - prior.Prepare,
		Exec:      s.Exec - prior.Exec,
		Query:     s.Query - prior.Query,
		StmtExec:  s.StmtExec - prior.StmtExec,
		StmtQuery: s.StmtQuery - prior.StmtQuery,
		Begin:     s.Begin - prior.Begin,
		Commit:    s.Commit - prior.Commit,
		Rollback:  s.Rollback - prior.Rollback,
	}
}

// Snapshot reads every counter. The read is not atomic ACROSS fields — a
// concurrent writer could interleave between two loads — which matches every
// other multi-atomic snapshot in this tree taken for one log line or report.
func (c *Counts) Snapshot() Snapshot {
	return Snapshot{
		Prepare:   atomic.LoadInt64(&c.prepare),
		Exec:      atomic.LoadInt64(&c.exec),
		Query:     atomic.LoadInt64(&c.query),
		StmtExec:  atomic.LoadInt64(&c.stmtExec),
		StmtQuery: atomic.LoadInt64(&c.stmtQuery),
		Begin:     atomic.LoadInt64(&c.begin),
		Commit:    atomic.LoadInt64(&c.commit),
		Rollback:  atomic.LoadInt64(&c.rollback),
	}
}

// Reset zeroes every counter, for reuse across benchmark iterations.
func (c *Counts) Reset() {
	atomic.StoreInt64(&c.prepare, 0)
	atomic.StoreInt64(&c.exec, 0)
	atomic.StoreInt64(&c.query, 0)
	atomic.StoreInt64(&c.stmtExec, 0)
	atomic.StoreInt64(&c.stmtQuery, 0)
	atomic.StoreInt64(&c.begin, 0)
	atomic.StoreInt64(&c.commit, 0)
	atomic.StoreInt64(&c.rollback, 0)
}

// Total is c.Snapshot().Total(), for a caller that wants one number without
// composing the two calls.
func (c *Counts) Total() int64 { return c.Snapshot().Total() }

// options carries WrapConnector's optional behavior.
type options struct {
	latency time.Duration
}

// Option configures WrapConnector.
type Option func(*options)

// WithLatency injects a sleep before every counted round trip — Prepare,
// Exec, Query, a prepared statement's Exec/Query, Begin, Commit and Rollback
// alike — so a co-located backend can approximate the wall-clock cost of a
// database that is NOT co-located with the caller. It respects the caller's
// context: a canceled or expired context aborts the sleep and returns the
// context's error rather than padding out a call that is already doomed.
func WithLatency(d time.Duration) Option {
	return func(o *options) { o.latency = d }
}

// WrapConnector wraps inner so every statement-shaped call on the
// connections it hands out increments counts, and optionally sleeps first.
// inner is the driver.Connector a backend already constructs
// (mysql.NewConnector for the Dolt server backend, doltembed.NewConnector
// for the embedded backend); wrapping it rather than registering a new
// driver under a new name is what lets a test open a SECOND, counted
// connection against the SAME already-migrated embedded directory or the
// SAME live Dolt server a backend's own store already opened, so the
// production open/migrate path never has to know this wrapper exists.
func WrapConnector(inner driver.Connector, counts *Counts, opts ...Option) driver.Connector {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return &wrappedConnector{inner: inner, counts: counts, opts: o}
}

type wrappedConnector struct {
	inner  driver.Connector
	counts *Counts
	opts   *options
}

func (c *wrappedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &wrappedConn{inner: inner, counts: c.counts, opts: c.opts}, nil
}

func (c *wrappedConnector) Driver() driver.Driver {
	return &wrappedDriver{inner: c.inner.Driver(), counts: c.counts, opts: c.opts}
}

// wrappedDriver exists only to satisfy driver.Connector.Driver's return
// type. database/sql calls Driver() for error classification and
// sql.DB.Driver(); this wrapper never uses it to open a second connection
// outside wrappedConnector.Connect.
type wrappedDriver struct {
	inner  driver.Driver
	counts *Counts
	opts   *options
}

func (d *wrappedDriver) Open(name string) (driver.Conn, error) {
	inner, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &wrappedConn{inner: inner, counts: d.counts, opts: d.opts}, nil
}

// sleep injects the configured latency, honoring ctx cancellation.
func (o *options) sleep(ctx context.Context) error {
	if o.latency <= 0 {
		return nil
	}
	t := time.NewTimer(o.latency)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wrappedConn wraps a driver.Conn. It implements every optional modern
// interface (ExecerContext, QueryerContext, ConnPrepareContext,
// ConnBeginTx, Pinger, SessionResetter, Validator, NamedValueChecker),
// delegating to the inner connection's implementation when present and
// falling back to the legacy path — or driver.ErrSkip — when not, so
// database/sql's behavior with the wrapper in place matches its behavior
// with the inner driver alone.
//
// NamedValueChecker is always implemented (never omitted) because
// database/sql decides parameter-conversion behavior by checking interface
// satisfaction on the WRAPPER's concrete type, not the inner driver's:
// omitting it here would silently change conversion behavior relative to
// running against the inner driver directly, regardless of what the inner
// driver itself implements.
type wrappedConn struct {
	inner  driver.Conn
	counts *Counts
	opts   *options
}

func (c *wrappedConn) Prepare(query string) (driver.Stmt, error) {
	atomic.AddInt64(&c.counts.prepare, 1)
	inner, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &wrappedStmt{inner: inner, counts: c.counts, opts: c.opts}, nil
}

func (c *wrappedConn) Close() error { return c.inner.Close() }

//nolint:staticcheck // legacy driver.Conn.Begin, required by the interface; BeginTx below is preferred when available.
func (c *wrappedConn) Begin() (driver.Tx, error) {
	atomic.AddInt64(&c.counts.begin, 1)
	inner, err := c.inner.Begin() //nolint:staticcheck
	if err != nil {
		return nil, err
	}
	return &wrappedTx{inner: inner, counts: c.counts}, nil
}

func (c *wrappedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	pc, ok := c.inner.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	if err := c.opts.sleep(ctx); err != nil {
		return nil, err
	}
	atomic.AddInt64(&c.counts.prepare, 1)
	inner, err := pc.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &wrappedStmt{inner: inner, counts: c.counts, opts: c.opts}, nil
}

func (c *wrappedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.opts.sleep(ctx); err != nil {
		return nil, err
	}
	atomic.AddInt64(&c.counts.exec, 1)
	return execer.ExecContext(ctx, query, args)
}

func (c *wrappedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.opts.sleep(ctx); err != nil {
		return nil, err
	}
	atomic.AddInt64(&c.counts.query, 1)
	return queryer.QueryContext(ctx, query, args)
}

func (c *wrappedConn) BeginTx(ctx context.Context, txOpts driver.TxOptions) (driver.Tx, error) {
	bt, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return c.Begin()
	}
	if err := c.opts.sleep(ctx); err != nil {
		return nil, err
	}
	atomic.AddInt64(&c.counts.begin, 1)
	inner, err := bt.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, err
	}
	return &wrappedTx{inner: inner, counts: c.counts}, nil
}

func (c *wrappedConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *wrappedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *wrappedConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *wrappedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if chk, ok := c.inner.(driver.NamedValueChecker); ok {
		return chk.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// wrappedStmt wraps a driver.Stmt the same way wrappedConn wraps a
// driver.Conn: delegate to a modern optional interface when the inner
// statement implements it, else fall back to the legacy path, and always
// implement NamedValueChecker for the reason documented on wrappedConn.
type wrappedStmt struct {
	inner  driver.Stmt
	counts *Counts
	opts   *options
}

func (s *wrappedStmt) Close() error  { return s.inner.Close() }
func (s *wrappedStmt) NumInput() int { return s.inner.NumInput() }

//nolint:staticcheck // legacy driver.Stmt.Exec, required by the interface and used as the ExecContext fallback below.
func (s *wrappedStmt) Exec(args []driver.Value) (driver.Result, error) {
	atomic.AddInt64(&s.counts.stmtExec, 1)
	return s.inner.Exec(args) //nolint:staticcheck
}

//nolint:staticcheck // legacy driver.Stmt.Query, required by the interface and used as the QueryContext fallback below.
func (s *wrappedStmt) Query(args []driver.Value) (driver.Rows, error) {
	atomic.AddInt64(&s.counts.stmtQuery, 1)
	return s.inner.Query(args) //nolint:staticcheck
}

func (s *wrappedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.opts.sleep(ctx); err != nil {
		return nil, err
	}
	if se, ok := s.inner.(driver.StmtExecContext); ok {
		atomic.AddInt64(&s.counts.stmtExec, 1)
		return se.ExecContext(ctx, args)
	}
	dargs, err := namedToOrdinal(args)
	if err != nil {
		return nil, err
	}
	atomic.AddInt64(&s.counts.stmtExec, 1)
	return s.inner.Exec(dargs) //nolint:staticcheck
}

func (s *wrappedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.opts.sleep(ctx); err != nil {
		return nil, err
	}
	if sq, ok := s.inner.(driver.StmtQueryContext); ok {
		atomic.AddInt64(&s.counts.stmtQuery, 1)
		return sq.QueryContext(ctx, args)
	}
	dargs, err := namedToOrdinal(args)
	if err != nil {
		return nil, err
	}
	atomic.AddInt64(&s.counts.stmtQuery, 1)
	return s.inner.Query(dargs) //nolint:staticcheck
}

func (s *wrappedStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if chk, ok := s.inner.(driver.NamedValueChecker); ok {
		return chk.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// namedToOrdinal converts modern named-value args back to the legacy
// ordinal-positioned driver.Value slice Exec/Query take, for an inner
// statement that implements neither StmtExecContext nor StmtQueryContext.
// Every driver this wrapper targets (mysql, dolthub/driver) implements the
// modern interfaces, so this path is exercised only as a defensive
// fallback, never in the measured code path.
func namedToOrdinal(args []driver.NamedValue) ([]driver.Value, error) {
	out := make([]driver.Value, len(args))
	for _, a := range args {
		if a.Name != "" {
			return nil, fmt.Errorf("sqlcount: named parameter %q not supported by the wrapped driver's legacy Exec/Query", a.Name)
		}
		if a.Ordinal < 1 || a.Ordinal > len(args) {
			return nil, fmt.Errorf("sqlcount: parameter ordinal %d out of range for %d args", a.Ordinal, len(args))
		}
		out[a.Ordinal-1] = a.Value
	}
	return out, nil
}

// wrappedTx wraps a driver.Tx purely to count Commit/Rollback; it does not
// change the transaction's behavior.
type wrappedTx struct {
	inner  driver.Tx
	counts *Counts
}

func (t *wrappedTx) Commit() error {
	atomic.AddInt64(&t.counts.commit, 1)
	return t.inner.Commit()
}

func (t *wrappedTx) Rollback() error {
	atomic.AddInt64(&t.counts.rollback, 1)
	return t.inner.Rollback()
}
