package pool

import (
	"context"
	"sync/atomic"

	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	querypb "github.com/dolthub/vitess/go/vt/proto/query"
	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// maxRows bounds how many rows a single ExecuteFetch buffers. bd result sets
// are small relative to this ceiling; it exists only to cap pathological reads.
const maxRows = 10_000_000

// Handler is a vitess mysql.Handler that pins one pooled backend per client
// connection and forwards every command to it. It is the QUERY-LEVEL serving
// core of the pooler (as opposed to the byte-passthrough proxy).
//
// Prepared statements: bd's server-mode DSN does NOT set interpolateParams, so
// go-sql-driver sends server-side COM_STMT_PREPARE/EXECUTE for every
// parameterized query. The vitess layer parses the prepare and binds the
// parameter values; this handler interpolates those bound values back into the
// SQL text (via vitess's own GenerateQuery, which SQL-encodes/escapes values)
// and runs the result as a plain text query on the backend. This makes
// prepared statements work without needing a server-side prepare on the
// backend (the vitess CLIENT conn exposes no prepared-statement API).
type Handler struct {
	pool   *Pool
	active atomic.Int64 // in-flight client connections, for graceful drain
}

// NewHandler returns a Handler backed by pool.
func NewHandler(pool *Pool) *Handler { return &Handler{pool: pool} }

// ActiveConns reports the number of client connections currently being served.
func (h *Handler) ActiveConns() int64 { return h.active.Load() }

// Ensure Handler satisfies the vitess interface at compile time.
var _ mysql.Handler = (*Handler)(nil)

// clientState is pinned to a client connection via c.ClientData for its
// lifetime. It tracks the borrowed backend and whether the session is still
// clean (so ConnectionClosed knows whether to Release or Discard).
type clientState struct {
	be      *backend
	tainted bool // an error occurred; backend must be discarded, not reused
}

func (h *Handler) stateFor(c *mysql.Conn) *clientState {
	st, _ := c.ClientData.(*clientState)
	return st
}

// NewConnection borrows a backend and pins it for this client's lifetime.
// If borrowing fails (pool exhausted/closed), the connection is left without a
// backend; the first command will fail cleanly via stateFor returning nil.
func (h *Handler) NewConnection(c *mysql.Conn) {
	h.active.Add(1)
	h.pool.cfg.Stats.IncClientConn()
	b, err := h.pool.Borrow(context.Background())
	if err != nil {
		h.pool.logf("client %d: borrow failed: %v", c.ConnectionID, err)
		c.ClientData = (*clientState)(nil)
		return
	}
	c.ClientData = &clientState{be: b}
}

// ConnectionClosed returns the backend to the pool: Release on a clean session
// (reset-and-reuse), Discard if any error tainted the session.
func (h *Handler) ConnectionClosed(c *mysql.Conn) {
	defer h.active.Add(-1)
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return
	}
	if st.tainted {
		h.pool.Discard(st.be)
	} else {
		h.pool.Release(st.be)
	}
	st.be = nil
}

func (h *Handler) ConnectionAuthenticated(c *mysql.Conn) error { return nil }

func (h *Handler) ConnectionAborted(c *mysql.Conn, reason string) error { return nil }

// ComInitDB handles USE <db> (and the implicit USE vitess issues after auth
// when the client handshake carried a default database).
func (h *Handler) ComInitDB(c *mysql.Conn, schemaName string) error {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return errNoBackend()
	}
	if err := st.be.useDB(schemaName); err != nil {
		st.tainted = true
		return err
	}
	return nil
}

// ComQuery forwards a text query to the pinned backend and streams the result.
func (h *Handler) ComQuery(ctx context.Context, c *mysql.Conn, query string, callback mysql.ResultSpoolFn) error {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return errNoBackend()
	}
	res, err := st.be.exec(query, maxRows)
	if err != nil {
		st.tainted = true
		return err
	}
	return callback(res, false)
}

// ComMultiQuery handles a single statement and returns the remainder. bd uses
// multiStatements=true in its DSN, so this path is exercised; vitess splits
// statements and calls us once per statement.
func (h *Handler) ComMultiQuery(ctx context.Context, c *mysql.Conn, query string, callback mysql.ResultSpoolFn) (string, error) {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return "", errNoBackend()
	}
	statement, remainder, err := sqlparser.SplitStatement(query)
	if err != nil {
		// Could not split; run the whole blob as one statement.
		statement, remainder = query, ""
	}
	res, qerr := st.be.exec(statement, maxRows)
	if qerr != nil {
		st.tainted = true
		return "", qerr
	}
	return remainder, callback(res, remainder != "")
}

// ComPrepare is invoked when a client sends COM_STMT_PREPARE. vitess has
// already parsed the query and counted its parameters; we return the result
// column fields. We cannot run the parameterized query yet (no values), and the
// vitess CLIENT conn has no prepared-statement API, so we return nil fields:
// go-sql-driver reads the authoritative column metadata from the binary
// COM_STMT_EXECUTE response instead, which we produce in ComStmtExecute.
func (h *Handler) ComPrepare(ctx context.Context, c *mysql.Conn, query string, prepare *mysql.PrepareData) ([]*querypb.Field, error) {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return nil, errNoBackend()
	}
	return nil, nil
}

// ComStmtExecute runs a previously prepared statement. vitess supplies the
// bound parameter values in prepare.BindVars (keyed v1, v2, ...). We parse the
// prepared SQL (its `?` placeholders become :v1, :v2 ...), interpolate the
// bound values via vitess's GenerateQuery (which SQL-encodes and escapes each
// value), and run the resulting plain text query on the backend.
func (h *Handler) ComStmtExecute(ctx context.Context, c *mysql.Conn, prepare *mysql.PrepareData, callback func(*sqltypes.Result) error) error {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return errNoBackend()
	}
	query, err := interpolate(prepare.PrepareStmt, prepare.BindVars)
	if err != nil {
		// Parse/interpolation failure does not desync the backend (nothing was
		// sent to it), so the session is NOT tainted.
		return mysql.NewSQLError(mysql.ERNotSupportedYet, mysql.SSUnknownSQLState,
			"pooler: cannot execute prepared statement: %v", err)
	}
	res, qerr := st.be.exec(query, maxRows)
	if qerr != nil {
		st.tainted = true
		return qerr
	}
	return callback(res)
}

// interpolate substitutes the bound parameter values into a parameterized
// statement, yielding an executable plain-text query.
func interpolate(stmt string, bindVars map[string]*querypb.BindVariable) (string, error) {
	parsed, err := sqlparser.Parse(stmt)
	if err != nil {
		return "", err
	}
	pq := sqlparser.NewParsedQuery(parsed)
	return pq.GenerateQuery(bindVars, nil)
}

// ComResetConnection forwards a client COM_RESET_CONNECTION to the backend so
// the backend's session state is cleared mid-connection too (go-sql-driver's
// pool sends this when recycling a conn).
func (h *Handler) ComResetConnection(c *mysql.Conn) error {
	st := h.stateFor(c)
	if st == nil || st.be == nil {
		return errNoBackend()
	}
	if err := st.be.reset(); err != nil {
		st.tainted = true
		return err
	}
	return nil
}

func (h *Handler) WarningCount(c *mysql.Conn) uint16 { return 0 }

func (h *Handler) ParserOptionsForConnection(c *mysql.Conn) (sqlparser.ParserOptions, error) {
	return sqlparser.ParserOptions{}, nil
}

func errNoBackend() error {
	return mysql.NewSQLError(mysql.ERUnknownError, mysql.SSUnknownSQLState,
		"pooler: no backend available for connection")
}
