// Package sqlgate wraps a real database/sql driver so a test can hold one
// operation at its commit point while another session commits.
//
// It exists for race characterization against a real Dolt sql-server: the
// operations under test keep their real SQL, transactions, wrapping and retry
// boundaries, and the gate only decides WHEN a labeled operation is allowed
// to commit. Nothing here fakes a result or an error.
//
// An operation is labeled by running it under WithLabel. For every label the
// gate counts the transactions the operation begins (a driver BeginTx or a
// START TRANSACTION statement) and, at the label's first commit point, records
// the session settings ProbeQuery reads on the very session that commits. A
// label that is Armed is additionally held at its first commit point until
// the test releases it. Later attempts of the same label are never held, so a
// replay runs to completion on its own.
//
// A commit point is the first of: driver Tx.Commit, a COMMIT statement, or a
// CALL DOLT_ADD / CALL DOLT_COMMIT statement. Holding there means the held
// transaction has already read and written everything it is going to, so its
// snapshot is pinned before the racing session commits.
package sqlgate

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

type labelKey struct{}

// WithLabel names the operation ctx belongs to.
func WithLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}

func labelOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	label, _ := ctx.Value(labelKey{}).(string)
	return label
}

// Hold is one armed label's handle.
type Hold struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

// Reached is closed once the held operation is waiting at its commit point.
func (h *Hold) Reached() <-chan struct{} { return h.reached }

// Release lets the held operation commit. It is safe to call more than once.
func (h *Hold) Release() { h.once.Do(func() { close(h.release) }) }

// Connector is a driver.Connector over a real driver and DSN.
type Connector struct {
	drv driver.Driver
	dsn string

	// ProbeQuery, when set, is run on a labeled session at its first commit
	// point; each column is recorded as a string under the column's name.
	ProbeQuery string

	mu       sync.Mutex
	holds    map[string]*Hold
	fired    map[string]bool
	attempts map[string]int
	settings map[string]map[string]string
	probeErr map[string]error
	kinds    map[string]string
}

// NewConnector wraps drv, opening every session with dsn.
func NewConnector(drv driver.Driver, dsn string) *Connector {
	return &Connector{
		drv:      drv,
		dsn:      dsn,
		holds:    map[string]*Hold{},
		fired:    map[string]bool{},
		attempts: map[string]int{},
		settings: map[string]map[string]string{},
		probeErr: map[string]error{},
		kinds:    map[string]string{},
	}
}

// Arm holds label's next first commit point until the returned Hold is
// released.
func (c *Connector) Arm(label string) *Hold {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := &Hold{reached: make(chan struct{}), release: make(chan struct{})}
	c.holds[label] = h
	delete(c.fired, label)
	return h
}

// Attempts reports how many transactions label has begun.
func (c *Connector) Attempts(label string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[label]
}

// Settings reports what ProbeQuery read on label's committing session, or the
// error the probe returned.
func (c *Connector) Settings(label string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.probeErr[label]; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(c.settings[label]))
	for k, v := range c.settings[label] {
		out[k] = v
	}
	return out, nil
}

// CommitKind names label's first commit point: "Tx.Commit", "COMMIT",
// "CALL DOLT_ADD" or "CALL DOLT_COMMIT". It is how a test reports the commit
// style an operation actually used rather than the one it asked for.
func (c *Connector) CommitKind(label string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kinds[label]
}

// Connect implements driver.Connector.
func (c *Connector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &conn{inner: inner, c: c}, nil
}

// Driver implements driver.Connector.
func (c *Connector) Driver() driver.Driver { return c.drv }

func (c *Connector) begun(ctx context.Context) {
	label := labelOf(ctx)
	if label == "" {
		return
	}
	c.mu.Lock()
	c.attempts[label]++
	c.mu.Unlock()
}

// commitPoint records label's session settings once and, when the label is
// armed and has not yet been held, waits for the release. A canceled ctx ends
// the wait so a failing test cannot strand the session.
func (c *Connector) commitPoint(ctx context.Context, raw driver.Conn, kind string) {
	label := labelOf(ctx)
	if label == "" {
		return
	}
	c.mu.Lock()
	if _, seen := c.kinds[label]; !seen {
		c.kinds[label] = kind
	}
	_, probed := c.settings[label]
	_, failed := c.probeErr[label]
	c.mu.Unlock()
	if !probed && !failed && c.ProbeQuery != "" {
		settings, err := probe(ctx, raw, c.ProbeQuery)
		c.mu.Lock()
		if err != nil {
			c.probeErr[label] = err
		} else {
			c.settings[label] = settings
		}
		c.mu.Unlock()
	}

	c.mu.Lock()
	h := c.holds[label]
	if h == nil || c.fired[label] {
		c.mu.Unlock()
		return
	}
	c.fired[label] = true
	c.mu.Unlock()

	close(h.reached)
	select {
	case <-h.release:
	case <-ctx.Done():
	}
}

func probe(ctx context.Context, raw driver.Conn, query string) (map[string]string, error) {
	q, ok := raw.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("sqlgate: driver connection cannot run the settings probe")
	}
	rows, err := q.QueryContext(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlgate: settings probe: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cols := rows.Columns()
	dest := make([]driver.Value, len(cols))
	if err := rows.Next(dest); err != nil {
		return nil, fmt.Errorf("sqlgate: settings probe row: %w", err)
	}
	out := make(map[string]string, len(cols))
	for i, col := range cols {
		switch v := dest[i].(type) {
		case nil:
			out[col] = "NULL"
		case []byte:
			out[col] = string(v)
		default:
			out[col] = fmt.Sprint(v)
		}
	}
	// Drain so the session is idle again before the caller's statement runs.
	// Only io.EOF ends the drain cleanly; any other error (a canceled ctx, a
	// dropped connection) is returned rather than retried, so the drain stays
	// bounded.
	for {
		err := rows.Next(dest)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("sqlgate: settings probe drain: %w", err)
		}
	}
}

func isBegin(query string) bool {
	q := strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(q, "START TRANSACTION") || strings.HasPrefix(q, "BEGIN")
}

// commitKind names the commit point query is, or "" for any other statement.
func commitKind(query string) string {
	q := strings.ToUpper(strings.TrimSpace(query))
	for _, kind := range []string{"COMMIT", "CALL DOLT_ADD", "CALL DOLT_COMMIT"} {
		if strings.HasPrefix(q, kind) {
			return kind
		}
	}
	return ""
}

// conn forwards every optional interface the go-sql-driver connection has, so
// database/sql drives it exactly as it drives the unwrapped one.
type conn struct {
	inner driver.Conn
	c     *Connector
}

var (
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
	_ driver.StmtExecContext    = (*stmt)(nil)
	_ driver.StmtQueryContext   = (*stmt)(nil)
	_ driver.NamedValueChecker  = (*stmt)(nil)
)

func (cn *conn) Prepare(query string) (driver.Stmt, error) {
	return cn.PrepareContext(context.Background(), query)
}

func (cn *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		s   driver.Stmt
		err error
	)
	if p, ok := cn.inner.(driver.ConnPrepareContext); ok {
		s, err = p.PrepareContext(ctx, query)
	} else {
		s, err = cn.inner.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &stmt{inner: s, cn: cn, query: query}, nil
}

func (cn *conn) Close() error { return cn.inner.Close() }

func (cn *conn) Begin() (driver.Tx, error) {
	return cn.BeginTx(context.Background(), driver.TxOptions{})
}

func (cn *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := cn.inner.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("sqlgate: driver connection has no BeginTx")
	}
	t, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	cn.c.begun(ctx)
	return &tx{inner: t, cn: cn, ctx: ctx}, nil
}

func (cn *conn) before(ctx context.Context, query string) {
	if isBegin(query) {
		cn.c.begun(ctx)
	}
	if kind := commitKind(query); kind != "" {
		cn.c.commitPoint(ctx, cn.inner, kind)
	}
}

func (cn *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := cn.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.before(ctx, query)
	return e.ExecContext(ctx, query, args)
}

func (cn *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := cn.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.before(ctx, query)
	return q.QueryContext(ctx, query, args)
}

func (cn *conn) Ping(ctx context.Context) error {
	if p, ok := cn.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (cn *conn) ResetSession(ctx context.Context) error {
	if r, ok := cn.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (cn *conn) IsValid() bool {
	if v, ok := cn.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (cn *conn) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := cn.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type tx struct {
	inner driver.Tx
	cn    *conn
	ctx   context.Context
}

func (t *tx) Commit() error {
	t.cn.c.commitPoint(t.ctx, t.cn.inner, "Tx.Commit")
	return t.inner.Commit()
}

func (t *tx) Rollback() error { return t.inner.Rollback() }

type stmt struct {
	inner driver.Stmt
	cn    *conn
	query string
}

func (s *stmt) Close() error  { return s.inner.Close() }
func (s *stmt) NumInput() int { return s.inner.NumInput() }

//nolint:staticcheck // driver.Stmt requires the deprecated Exec/Query pair.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) { return s.inner.Exec(args) }

//nolint:staticcheck // driver.Stmt requires the deprecated Exec/Query pair.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) { return s.inner.Query(args) }

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	e, ok := s.inner.(driver.StmtExecContext)
	if !ok {
		return nil, errors.New("sqlgate: driver statement has no ExecContext")
	}
	s.cn.before(ctx, s.query)
	return e.ExecContext(ctx, args)
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := s.inner.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("sqlgate: driver statement has no QueryContext")
	}
	s.cn.before(ctx, s.query)
	return q.QueryContext(ctx, args)
}

func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}
