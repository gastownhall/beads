// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// Dolt provides native version control for SQL data with cell-level merge, history queries,
// and federation via Dolt remotes. The database itself is version-controlled.
//
// Dolt capabilities:
//   - Native version control (commit, push, pull, branch, merge)
//   - Time-travel queries via AS OF and dolt_history_* tables
//   - Cell-level merge for conflict resolution
//   - Multi-writer via dolt sql-server (federation, pure Go)
//
// All operations require a running dolt sql-server. Connect via MySQL protocol (pure Go).
package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DefaultSQLPort is the default port for dolt sql-server.
const DefaultSQLPort = 3307

// testDatabasePrefixes are name prefixes that indicate a test database.
// Used by isTestDatabaseName to prevent test databases from being created
// on the production Dolt server (Clown Shows #12-#18).
var testDatabasePrefixes = []string{
	"testdb_",
	"beads_t",
	"beads_pt",
	"beads_vr",
	"doctest_",
	"doctortest_",
}

// isTestDatabaseName returns true if the database name matches known test patterns.
// This is a pattern-based firewall — it does not rely on environment variables.
func isTestDatabaseName(name string) bool {
	for _, prefix := range testDatabasePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db       *sql.DB
	dbPath   string       // Path to Dolt database directory
	closed   atomic.Bool  // Tracks whether Close() has been called
	connStr  string       // Connection string for reconnection
	mu       sync.RWMutex // Protects concurrent access
	readOnly      bool   // True if opened in read-only mode
	credentialKey []byte // Random encryption key for federation credentials

	// Per-invocation caches (lifetime = DoltStore lifetime)
	customStatusCache  []string        // cached result of GetCustomStatuses
	customStatusCached bool            // true once customStatusCache has been populated
	customTypeCache    []string        // cached result of GetCustomTypes
	customTypeCached   bool            // true once customTypeCache has been populated
	infraTypeCache     map[string]bool // cached result of GetInfraTypes
	infraTypeCached    bool            // true once infraTypeCache has been populated
	blockedIDsCache    []string        // cached result of computeBlockedIDs
	blockedIDsCacheMap map[string]bool
	blockedIDsCached   bool // true once blockedIDsCache has been populated
	cacheMu            sync.Mutex

	// OTel span attribute cache (avoids per-call allocation)
	spanAttrsOnce  sync.Once
	spanAttrsCache []attribute.KeyValue

	// Circuit breaker for Dolt server connections
	breaker *circuitBreaker

	// Version control config
	committerName  string
	committerEmail string
	remote         string // Default remote for push/pull
	branch         string // Current branch
	remoteUser     string // Remote auth user for Hosted Dolt push/pull (optional)
	remotePassword string // Remote auth password for Hosted Dolt push/pull (optional)
}

// Config holds Dolt database configuration
type Config struct {
	Path           string // Path to Dolt database directory
	BeadsDir       string // Path to .beads directory (for server auto-start when Path is custom)
	CommitterName  string // Git-style committer name
	CommitterEmail string // Git-style committer email
	Remote         string // Default remote name (e.g., "origin")
	Database       string // Database name within Dolt (default: "beads")
	ReadOnly       bool   // Open in read-only mode (skip schema init)

	// Server connection options
	ServerHost     string // Server host (default: 127.0.0.1)
	ServerPort     int    // Server port (default: 3307)
	ServerUser     string // MySQL user (default: root)
	ServerPassword string // MySQL password (default: empty, can be set via BEADS_DOLT_PASSWORD)
	ServerTLS      bool   // Enable TLS for server connections (required for Hosted Dolt)

	// Remote auth for Hosted Dolt push/pull (optional)
	// When set, Push/Pull use the --user flag and set DOLT_REMOTE_PASSWORD env var.
	RemoteUser     string // Hosted Dolt remote user (set via DOLT_REMOTE_USER env var)
	RemotePassword string // Hosted Dolt remote password (set via DOLT_REMOTE_PASSWORD env var)

	// CreateIfMissing allows CREATE DATABASE when the target database does not
	// exist on the server. Only explicit initialization, migration, or new-board
	// creation paths should set this to true. Normal open paths leave it false,
	// which causes an error if the database is missing — preventing silent
	// creation of shadow databases on the wrong server.
	CreateIfMissing bool

	// AutoStart enables transparent server auto-start when connection fails.
	// When true and the host is localhost, bd will start a dolt sql-server
	// automatically if one isn't running. Disabled under Gas Town (GT_ROOT set).
	AutoStart bool

	// MaxOpenConns overrides the connection pool size (0 = default 10).
	// Set to 1 for branch isolation in tests (DOLT_CHECKOUT is session-level).
	MaxOpenConns int
}

// cliExecTimeout is the maximum time to wait for dolt CLI push/pull operations.
// SSH transfers can hang indefinitely on network issues or SSH key prompts;
// this prevents the process from blocking forever.
const cliExecTimeout = 5 * time.Minute

// Retry configuration for transient connection errors (stale pool connections,
// brief network issues, server restarts).
const serverRetryMaxElapsed = 30 * time.Second

func newServerRetryBackoff() backoff.BackOff {
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = serverRetryMaxElapsed
	return bo
}

// isRetryableError returns true if the error is a transient connection error
// that should be retried in server mode.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// MySQL driver transient errors
	if strings.Contains(errStr, "driver: bad connection") {
		return true
	}
	if strings.Contains(errStr, "invalid connection") {
		return true
	}
	// Network transient errors (brief blips, not persistent failures)
	if strings.Contains(errStr, "broken pipe") {
		return true
	}
	if strings.Contains(errStr, "connection reset") {
		return true
	}
	// Server restart: "connection refused" is transient — the server may
	// come back within the backoff window (30s). Retrying here prevents
	// a brief server outage from cascading into permanent failures.
	if strings.Contains(errStr, "connection refused") {
		return true
	}
	// Dolt read-only mode: under load, Dolt may enter read-only mode with
	// "cannot update manifest: database is read only". This clears after
	// a server restart, so it's worth retrying.
	if strings.Contains(errStr, "database is read only") {
		return true
	}
	// MySQL error 2013: mid-query disconnect
	if strings.Contains(errStr, "lost connection") {
		return true
	}
	// MySQL error 2006: idle connection timeout
	if strings.Contains(errStr, "gone away") {
		return true
	}
	// Go net package timeout on read/write
	if strings.Contains(errStr, "i/o timeout") {
		return true
	}
	// Dolt server catalog race: after CREATE DATABASE, the server's in-memory
	// catalog may not have registered the new database yet. The immediately
	// following USE (implicit via DSN) fails with "Unknown database". This is
	// transient and resolves once the catalog refreshes. (GH-1851)
	if strings.Contains(errStr, "unknown database") {
		return true
	}
	// Dolt internal race: after CREATE DATABASE, information_schema queries
	// on the new database may fail with "no root value found in session" if
	// the server hasn't finished initializing the database's root value.
	// This is transient and resolves on retry.
	if strings.Contains(errStr, "no root value found") {
		return true
	}
	return false
}

// isLockError returns true if the error indicates a Dolt lock contention problem.
// These can occur when the Dolt server's storage layer is locked by another
// process or a stale LOCK file was left behind by a crashed server.
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "lock file") ||
		strings.Contains(errStr, "noms lock") ||
		strings.Contains(errStr, "locked by another dolt process")
}

// wrapLockError wraps lock-related errors with actionable guidance.
// Non-lock errors and nil are returned unchanged.
func wrapLockError(err error) error {
	if !isLockError(err) {
		return err
	}
	return fmt.Errorf("%w\n\nThe Dolt database is locked. This usually means the Dolt server's "+
		"storage is held by another process or a stale lock file exists.\n"+
		"Try restarting the Dolt server, or run 'bd doctor --fix' to clean stale lock files.", err)
}

// withRetry executes an operation with retry for transient errors.
// If a circuit breaker is configured, it checks the breaker before each attempt
// and records connection failures/successes to coordinate fail-fast across processes.
func (s *DoltStore) withRetry(ctx context.Context, op func() error) error {
	// Circuit breaker: fail-fast if the server is known to be down.
	if s.breaker != nil && !s.breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return ErrCircuitOpen
	}

	attempts := 0
	bo := newServerRetryBackoff()
	err := backoff.Retry(func() error {
		attempts++
		err := op()
		if err != nil && isRetryableError(err) {
			// Record connection-level failures to the circuit breaker
			if s.breaker != nil && isConnectionError(err) {
				s.breaker.RecordFailure()
				// Check if the breaker just tripped — if so, stop retrying
				if s.breaker.State() == circuitOpen {
					doltMetrics.circuitTrips.Add(ctx, 1)
					return backoff.Permanent(fmt.Errorf("%w (circuit breaker tripped)", err))
				}
			}
			return err // Retryable - backoff will retry
		}
		if err != nil {
			return backoff.Permanent(err) // Non-retryable - stop immediately
		}
		// Success — reset the circuit breaker
		if s.breaker != nil {
			s.breaker.RecordSuccess()
		}
		return nil
	}, backoff.WithContext(bo, ctx))
	if attempts > 1 {
		doltMetrics.retryCount.Add(ctx, int64(attempts-1))
	}
	return err
}

// doltTracer is the OTel tracer for SQL-level spans.
// It uses the global provider, which is a no-op until telemetry.Init() is called.
var doltTracer = otel.Tracer("github.com/steveyegge/beads/storage/dolt")

// doltMetrics holds OTel metric instruments for the dolt storage backend.
// Instruments are registered against the global delegating provider at init time,
// so they automatically forward to the real provider once telemetry.Init() runs.
var doltMetrics struct {
	retryCount      metric.Int64Counter
	lockWaitMs      metric.Float64Histogram
	circuitTrips    metric.Int64Counter
	circuitRejected metric.Int64Counter
}

func init() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	doltMetrics.retryCount, _ = m.Int64Counter("bd.db.retry_count",
		metric.WithDescription("SQL operations retried due to server-mode transient errors"),
		metric.WithUnit("{retry}"),
	)
	doltMetrics.lockWaitMs, _ = m.Float64Histogram("bd.db.lock_wait_ms",
		metric.WithDescription("Time spent waiting to acquire the dolt access lock"),
		metric.WithUnit("ms"),
	)
	doltMetrics.circuitTrips, _ = m.Int64Counter("bd.db.circuit_trips",
		metric.WithDescription("Number of times the Dolt circuit breaker tripped open"),
		metric.WithUnit("{trip}"),
	)
	doltMetrics.circuitRejected, _ = m.Int64Counter("bd.db.circuit_rejected",
		metric.WithDescription("Requests rejected by open circuit breaker (fail-fast)"),
		metric.WithUnit("{request}"),
	)
}

// doltSpanAttrs returns the fixed attributes shared by all SQL spans.
// Cached to avoid allocating on every call (hot path when telemetry is disabled
// still flows through no-op tracers).
func (s *DoltStore) doltSpanAttrs() []attribute.KeyValue {
	s.spanAttrsOnce.Do(func() {
		s.spanAttrsCache = []attribute.KeyValue{
			attribute.String("db.system", "dolt"),
			attribute.Bool("db.readonly", s.readOnly),
			attribute.Bool("db.server_mode", true), // always server mode after embedded removal
		}
	})
	return s.spanAttrsCache
}

// spanSQL truncates a SQL string to keep spans readable.
func spanSQL(q string) string {
	if len(q) > 300 {
		return q[:300] + "…"
	}
	return q
}

// endSpan records an error (if any) and ends the span.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// execContext wraps a write statement in an explicit BEGIN/COMMIT to ensure
// durability when the Dolt server runs with autocommit disabled (the default
// when started with --no-auto-commit). Without this, writes remain in an
// ErrStoreClosed is returned when an operation is attempted on a closed store.
var ErrStoreClosed = errors.New("store is closed")

// uncommitted implicit transaction that Dolt rolls back on connection close,
// causing silent data loss for callers that do not use db.BeginTx themselves.
func (s *DoltStore) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.exec",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "exec"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	var result sql.Result
	err := s.withRetry(ctx, func() error {
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		var execErr error
		result, execErr = tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			_ = tx.Rollback()
			return execErr
		}
		return tx.Commit()
	})
	finalErr := wrapLockError(err)
	endSpan(span, finalErr)
	return result, finalErr
}

// DB returns the underlying sql.DB connection for direct queries.
// Use sparingly — prefer the store's typed methods for normal operations.
func (s *DoltStore) DB() *sql.DB {
	return s.db
}

// QueryContext wraps s.db.QueryContext with retry for transient errors.
// Exported so callers (e.g. backup) can run ad-hoc queries with retry
// instead of going through the raw *sql.DB.
func (s *DoltStore) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.queryContext(ctx, query, args...)
}

// queryContext wraps s.db.QueryContext with retry for transient errors.
func (s *DoltStore) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "query"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	var rows *sql.Rows
	err := s.withRetry(ctx, func() error {
		// Close any Rows from a previous failed attempt to avoid leaking connections.
		if rows != nil {
			_ = rows.Close()
			rows = nil
		}
		var queryErr error
		rows, queryErr = s.db.QueryContext(ctx, query, args...)
		return queryErr
	})
	finalErr := wrapLockError(err)
	endSpan(span, finalErr)
	return rows, finalErr
}

// queryRowContext wraps s.db.QueryRowContext with retry for transient errors.
// The scan function receives the *sql.Row and should call .Scan() on it.
func (s *DoltStore) queryRowContext(ctx context.Context, scan func(*sql.Row) error, query string, args ...any) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.query_row",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "query_row"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	finalErr := wrapLockError(s.withRetry(ctx, func() error {
		row := s.db.QueryRowContext(ctx, query, args...)
		return scan(row)
	}))
	endSpan(span, finalErr)
	return finalErr
}

// applyConfigDefaults fills in default values for unset Config fields.
func applyConfigDefaults(cfg *Config) {
	if cfg.Database == "" {
		if os.Getenv("BEADS_TEST_MODE") == "1" && cfg.Path != "" {
			// Test mode: derive unique database name from path for isolation.
			// Each test creates a unique temp directory, so hashing the path
			// gives each test its own database on the shared test server.
			h := fnv.New64a()
			_, _ = h.Write([]byte(cfg.Path)) // hash.Hash.Write never returns an error
			cfg.Database = fmt.Sprintf("testdb_%x", h.Sum64())
		} else {
			cfg.Database = configfile.DefaultDoltDatabase
		}
	}
	if cfg.CommitterName == "" {
		cfg.CommitterName = os.Getenv("GIT_AUTHOR_NAME")
		if cfg.CommitterName == "" {
			cfg.CommitterName = "beads"
		}
	}
	if cfg.CommitterEmail == "" {
		cfg.CommitterEmail = os.Getenv("GIT_AUTHOR_EMAIL")
		if cfg.CommitterEmail == "" {
			cfg.CommitterEmail = "beads@local"
		}
	}
	if cfg.Remote == "" {
		cfg.Remote = "origin"
	}

	// Server connection defaults (always applied — server mode is the only mode)
	if cfg.ServerHost == "" {
		// Host resolution: BEADS_DOLT_SERVER_HOST env > default 127.0.0.1.
		if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
			cfg.ServerHost = h
		} else {
			cfg.ServerHost = "127.0.0.1"
		}
	}
	// Port resolution: BEADS_DOLT_SERVER_PORT env (or legacy BEADS_DOLT_PORT) >
	// BEADS_TEST_MODE guard > metadata config > default.
	// CRITICAL: BEADS_TEST_MODE=1 forces port 1 (immediate fail) if the resolved port
	// is the production port (DefaultSQLPort). This prevents test databases from leaking
	// onto production even when the port env var is set to 3307 by Gas Town's beads module.
	// Only an explicit non-production port (e.g., 43211 for a test server)
	// overrides test mode — that's a deliberate test server assignment.
	envPort := os.Getenv("BEADS_DOLT_SERVER_PORT")
	if envPort == "" {
		envPort = os.Getenv("BEADS_DOLT_PORT") // legacy fallback
	}
	if envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.ServerPort = p
		}
	} else if cfg.ServerPort == 0 {
		cfg.ServerPort = DefaultSQLPort
	}
	// Test mode guard: if we'd hit production, force port 1 instead.
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		if cfg.ServerPort == 0 || cfg.ServerPort == DefaultSQLPort {
			cfg.ServerPort = 1
		}
	}
	if cfg.ServerUser == "" {
		cfg.ServerUser = "root"
	}
	// Check environment variable for password (more secure than command-line)
	if cfg.ServerPassword == "" {
		cfg.ServerPassword = os.Getenv("BEADS_DOLT_PASSWORD")
	}

	// Remote credentials for Hosted Dolt push/pull (env vars take precedence)
	if cfg.RemoteUser == "" {
		cfg.RemoteUser = os.Getenv("DOLT_REMOTE_USER")
	}
	if cfg.RemotePassword == "" {
		cfg.RemotePassword = os.Getenv("DOLT_REMOTE_PASSWORD")
	}
}

// New creates a new Dolt storage backend.
// Connects to a running dolt sql-server via MySQL protocol (pure Go).
func New(ctx context.Context, cfg *Config) (*DoltStore, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("database path is required")
	}

	applyConfigDefaults(cfg)

	// Hard guard: tests must NEVER connect to the production Dolt server.
	// If BEADS_TEST_MODE=1 and we're about to hit the default prod port,
	// something upstream forgot to set BEADS_DOLT_SERVER_PORT. Panic immediately
	// so the test fails loudly instead of silently polluting prod.
	if os.Getenv("BEADS_TEST_MODE") == "1" && cfg.ServerPort == DefaultSQLPort {
		panic(fmt.Sprintf(
			"BEADS_TEST_MODE=1 but connecting to prod port %d — set BEADS_DOLT_SERVER_PORT or use test helpers (database=%q, path=%q)",
			DefaultSQLPort, cfg.Database, cfg.Path,
		))
	}

	return newServerMode(ctx, cfg)
}

// newServerMode creates a DoltStore connected to a running dolt sql-server.
// This path is pure Go and does not require CGO.
func newServerMode(ctx context.Context, cfg *Config) (*DoltStore, error) {
	breaker := newCircuitBreaker(cfg.ServerPort)

	// Circuit breaker: fail-fast if the server is known to be down.
	if !breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return nil, ErrCircuitOpen
	}

	// Fail-fast TCP check before MySQL protocol initialization.
	// This gives an immediate, clear error if the Dolt server isn't running,
	// rather than waiting for MySQL driver timeouts.
	addr := net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
	conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dialErr != nil {
		// Auto-start: if enabled and connecting to localhost, start a server
		if cfg.AutoStart && isLocalHost(cfg.ServerHost) && cfg.Path != "" {
			beadsDir := cfg.BeadsDir
			if beadsDir == "" {
				beadsDir = filepath.Dir(cfg.Path) // fallback: cfg.Path is .beads/dolt → parent is .beads/
			}
			port, startErr := doltserver.EnsureRunning(beadsDir)
			if startErr != nil {
				return nil, fmt.Errorf("Dolt server unreachable at %s and auto-start failed: %w\n\n"+
					"To start manually: bd dolt start\n"+
					"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml",
					addr, startErr)
			}
			// Update port in case EnsureRunning used a derived port
			if port != cfg.ServerPort {
				cfg.ServerPort = port
				addr = net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
			}
			// Retry connection with longer timeout (server just started)
			conn, dialErr = net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr != nil {
				breaker.RecordFailure()
				return nil, fmt.Errorf("Dolt server auto-started but still unreachable at %s: %w\n\n"+
					"Check logs: %s", addr, dialErr, doltserver.LogPath(beadsDir))
			}
		} else if doltserver.IsDaemonManaged() && os.Getenv("BEADS_TEST_MODE") != "1" {
			// Gas Town detected (GT_ROOT or filesystem heuristic) — delegate
			// server start to gt, which manages the lifecycle properly.
			// Skip in test mode: tests manage their own server via testutil.StartTestDoltServer.
			gtBin, lookErr := exec.LookPath("gt")
			if lookErr != nil {
				return nil, fmt.Errorf("Dolt server unreachable at %s (Gas Town detected but 'gt' not found in PATH): %w\n\n"+
					"Start the server with: gt dolt start", addr, dialErr)
			}
			cmd := exec.CommandContext(ctx, gtBin, "dolt", "start")
			if out, runErr := cmd.CombinedOutput(); runErr != nil {
				return nil, fmt.Errorf("Dolt server unreachable at %s and 'gt dolt start' failed: %w\n\ngt output: %s",
					addr, runErr, strings.TrimSpace(string(out)))
			}
			// Retry connection after gt started the server
			conn, dialErr = net.DialTimeout("tcp", addr, 3*time.Second)
			if dialErr != nil {
				breaker.RecordFailure()
				return nil, fmt.Errorf("Dolt server still unreachable at %s after 'gt dolt start': %w",
					addr, dialErr)
			}
		} else {
			breaker.RecordFailure()
			return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\nThe Dolt server may not be running. Try:\n  bd dolt start    # Start a local server\n  gt dolt start    # If using Gas Town",
				addr, dialErr)
		}
	}
	_ = conn.Close()
	// TCP dial succeeded — record success to reset the breaker
	breaker.RecordSuccess()

	// Server mode: connect via MySQL protocol to dolt sql-server
	db, connStr, err := openServerConnection(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping Dolt database: %w", err)
	}

	store := &DoltStore{
		db:             db,
		dbPath:         cfg.Path,
		connStr:        connStr,
		breaker:        breaker,
		committerName:  cfg.CommitterName,
		committerEmail: cfg.CommitterEmail,
		remote:         cfg.Remote,
		branch:         "main",
		remoteUser:     cfg.RemoteUser,
		remotePassword: cfg.RemotePassword,
		readOnly:       cfg.ReadOnly,
	}

	// Schema initialization for server mode (idempotent).
	// Short retry for Dolt "no root value found in session" race: after
	// CREATE DATABASE, information_schema queries may fail transiently
	// even though Ping succeeded. This resolves within ~1s.
	if !cfg.ReadOnly {
		schemaBO := backoff.NewExponentialBackOff()
		schemaBO.InitialInterval = 100 * time.Millisecond
		schemaBO.MaxElapsedTime = 5 * time.Second
		if err := backoff.Retry(func() error {
			schemaErr := store.initSchema(ctx)
			if schemaErr != nil && isRetryableError(schemaErr) {
				return schemaErr
			}
			if schemaErr != nil {
				return backoff.Permanent(schemaErr)
			}
			return nil
		}, backoff.WithContext(schemaBO, ctx)); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	// Initialize credential encryption key (loads from file or generates new random key).
	// This must run after schema init so federation_peers table exists for migration.
	if !cfg.ReadOnly {
		if err := store.initCredentialKey(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize credential key: %w", err)
		}
	}

	// All writers operate on main — transaction isolation via RunInTransaction
	// replaces the former branch-per-polecat approach (BD_BRANCH).
	store.branch = "main"

	return store, nil
}

// isLocalHost returns true if the host refers to the local machine.
func isLocalHost(host string) bool {
	switch host {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// buildServerDSN constructs a MySQL DSN for connecting to a Dolt server.
// If database is empty, connects without selecting a database (for init operations).
func buildServerDSN(cfg *Config, database string) string {
	var userPart string
	if cfg.ServerPassword != "" {
		userPart = fmt.Sprintf("%s:%s", cfg.ServerUser, cfg.ServerPassword)
	} else {
		userPart = cfg.ServerUser
	}

	var dbPart string
	if database != "" {
		dbPart = "/" + database
	} else {
		dbPart = "/"
	}

	// Timeouts prevent agents from blocking forever when Dolt server hangs.
	// timeout=5s: TCP connect timeout
	// readTimeout=10s: I/O read timeout (covers hung queries)
	// writeTimeout=10s: I/O write timeout
	params := "parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=10s"
	if cfg.ServerTLS {
		params += "&tls=true"
	}

	return fmt.Sprintf("%s@tcp(%s:%d)%s?%s",
		userPart, cfg.ServerHost, cfg.ServerPort, dbPart, params)
}

// openServerConnection opens a connection to a dolt sql-server via MySQL protocol
func openServerConnection(ctx context.Context, cfg *Config) (*sql.DB, string, error) {
	connStr := buildServerDSN(cfg, cfg.Database)

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open Dolt server connection: %w", err)
	}

	// Server mode supports multi-writer, configure reasonable pool size
	maxOpen := 10
	if cfg.MaxOpenConns > 0 {
		maxOpen = cfg.MaxOpenConns
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(min(5, maxOpen))
	db.SetConnMaxLifetime(5 * time.Minute)

	// Ensure database exists (may need to create it)
	// First connect without database to create it
	initConnStr := buildServerDSN(cfg, "")
	initDB, err := sql.Open("mysql", initConnStr)
	if err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("failed to open init connection: %w", err)
	}
	defer func() { _ = initDB.Close() }()

	// Validate database name to prevent SQL injection via backtick escaping
	if err := ValidateDatabaseName(cfg.Database); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("invalid database name %q: %w", cfg.Database, err)
	}

	// FIREWALL: Never create test databases on the production server.
	// This is the last line of defense against test pollution (Clown Shows #12-#18).
	// Pattern-based, not env-var-based — env vars can be misconfigured or missing.
	if isTestDatabaseName(cfg.Database) && cfg.ServerPort == DefaultSQLPort {
		_ = db.Close()
		return nil, "", fmt.Errorf(
			"REFUSED: will not CREATE DATABASE %q on production port %d — "+
				"this is a test database name on the production server (see DOLT-WAR-ROOM.md)",
			cfg.Database, cfg.ServerPort)
	}

	// Check if the database already exists before deciding whether to create it.
	// This prevents the shadow database bug: without CreateIfMissing, connecting
	// to a server that lacks the expected database is an error (not silent creation).
	//
	// Uses SHOW DATABASES + iterate for exact match instead of SHOW DATABASES LIKE,
	// because LIKE treats _ and % as wildcards and Dolt does not support backslash
	// escaping. Database names like "beads_vulcan" contain underscores which would
	// match unrelated databases with LIKE.
	dbExists, checkErr := databaseExistsOnServer(ctx, initDB, cfg.Database)
	if checkErr != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("failed to check if database %q exists on server %s:%d: %w",
			cfg.Database, cfg.ServerHost, cfg.ServerPort, checkErr)
	}

	if !dbExists {
		if !cfg.CreateIfMissing {
			_ = db.Close()
			return nil, "", fmt.Errorf(
				"database %q not found on Dolt server at %s:%d\n\n"+
					"This can happen when:\n"+
					"  - The server is serving a different data directory than expected\n"+
					"  - The database has not been initialized yet\n\n"+
					"To initialize a new board:  bd init\n"+
					"To check server status:     bd doctor",
				cfg.Database, cfg.ServerHost, cfg.ServerPort)
		}

		_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database)) //nolint:gosec // G201: cfg.Database validated by ValidateDatabaseName above
		if err != nil {
			// Dolt may return error 1007 even with IF NOT EXISTS - ignore if database already exists
			errLower := strings.ToLower(err.Error())
			if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
				_ = db.Close()
				// Check for connection refused - server likely not running
				if strings.Contains(errLower, "connection refused") || strings.Contains(errLower, "connect: connection refused") {
					return nil, "", fmt.Errorf("failed to connect to Dolt server at %s:%d: %w\n\nThe Dolt server may not be running. Try:\n  bd dolt start    # Start a local server\n  gt dolt start    # If using Gas Town",
						cfg.ServerHost, cfg.ServerPort, err)
				}
				return nil, "", fmt.Errorf("failed to create database: %w", err)
			}
			// Database already exists - that's fine, continue
		}
	}

	// Wait for the Dolt server's in-memory catalog to register the new database.
	// After CREATE DATABASE, there is a race where the server has created the
	// database on disk but hasn't updated its catalog yet. Pinging db (which
	// has the database in the DSN) will fail with "Unknown database" until the
	// catalog catches up. We retry with exponential backoff. (GH-1851)
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 100 * time.Millisecond
	bo.MaxElapsedTime = 10 * time.Second
	if err := backoff.Retry(func() error {
		pingErr := db.PingContext(ctx)
		if pingErr != nil && isRetryableError(pingErr) {
			return pingErr // retryable — backoff will retry
		}
		if pingErr != nil {
			return backoff.Permanent(pingErr)
		}
		return nil
	}, backoff.WithContext(bo, ctx)); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("database %q not available after CREATE DATABASE: %w", cfg.Database, err)
	}

	return db, connStr, nil
}

// databaseExistsOnServer checks if a database with the exact given name exists
// on the Dolt server. Uses SHOW DATABASES + iterate instead of SHOW DATABASES LIKE
// to avoid LIKE wildcard issues with underscores in database names.
func databaseExistsOnServer(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return false, err
		}
		if dbName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// initSchema creates all tables if they don't exist
func initSchemaOnDB(ctx context.Context, db *sql.DB) error {
	// Fast path: if schema is already at current version, skip initialization.
	// This avoids ~20 DDL statements per bd invocation when schema is current.
	var version int
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version)
	if err == nil && version >= currentSchemaVersion {
		return nil
	}

	// Execute schema creation - split into individual statements
	// because MySQL/Dolt doesn't support multiple statements in one Exec
	for _, stmt := range splitStatements(schema) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		// Skip pure comment-only statements, but execute statements that start with comments
		if isOnlyComments(stmt) {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to create schema: %w\nStatement: %s", err, truncateForError(stmt))
		}
	}

	// Insert default config values
	for _, stmt := range splitStatements(defaultConfig) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if isOnlyComments(stmt) {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to insert default config: %w", err)
		}
	}

	// Apply index migrations for existing databases.
	// CREATE TABLE IF NOT EXISTS won't add new indexes to existing tables.
	indexMigrations := []string{
		"CREATE INDEX idx_issues_issue_type ON issues(issue_type)",
	}
	for _, migration := range indexMigrations {
		_, err := db.ExecContext(ctx, migration)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate") &&
			!strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fmt.Errorf("failed to apply index migration: %w", err)
		}
	}

	// Remove FK constraint on depends_on_id to allow external references.
	// This is idempotent - DROP FOREIGN KEY fails silently if constraint doesn't exist.
	_, err = db.ExecContext(ctx, "ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_depends_on")
	if err == nil {
		// DDL change succeeded - commit it so it persists (required for Dolt server mode)
		_, _ = db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'migration: remove fk_dep_depends_on for external references')") // Best effort: migration commit is advisory; schema change already applied
	} else if !strings.Contains(strings.ToLower(err.Error()), "can't drop") &&
		!strings.Contains(strings.ToLower(err.Error()), "doesn't exist") &&
		!strings.Contains(strings.ToLower(err.Error()), "check that it exists") &&
		!strings.Contains(strings.ToLower(err.Error()), "was not found") {
		return fmt.Errorf("failed to drop fk_dep_depends_on: %w", err)
	}

	// Create views
	if _, err := db.ExecContext(ctx, readyIssuesView); err != nil {
		return fmt.Errorf("failed to create ready_issues view: %w", err)
	}
	if _, err := db.ExecContext(ctx, blockedIssuesView); err != nil {
		return fmt.Errorf("failed to create blocked_issues view: %w", err)
	}

	// Run schema migrations for existing databases (bd-ijw)
	if err := RunMigrations(db); err != nil {
		return fmt.Errorf("failed to run dolt migrations: %w", err)
	}

	// Mark schema as current so subsequent invocations skip initialization
	_, _ = db.ExecContext(ctx,
		"INSERT INTO config (`key`, `value`) VALUES ('schema_version', ?) "+
			"ON DUPLICATE KEY UPDATE `value` = ?",
		currentSchemaVersion, currentSchemaVersion)

	return nil
}

func (s *DoltStore) initSchema(ctx context.Context) error {
	return initSchemaOnDB(ctx, s.db)
}

// splitStatements splits a SQL script into individual statements
func splitStatements(script string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	stringChar := byte(0)

	for i := 0; i < len(script); i++ {
		c := script[i]

		if inString {
			current.WriteByte(c)
			if c == stringChar && (i == 0 || script[i-1] != '\\') {
				inString = false
			}
			continue
		}

		if c == '\'' || c == '"' || c == '`' {
			inString = true
			stringChar = c
			current.WriteByte(c)
			continue
		}

		if c == ';' {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteByte(c)
	}

	// Handle last statement without semicolon
	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

// truncateForError truncates a string for use in error messages
func truncateForError(s string) string {
	if len(s) > 100 {
		return s[:100] + "..."
	}
	return s
}

// isOnlyComments returns true if the statement contains only SQL comments
func isOnlyComments(stmt string) bool {
	lines := strings.Split(stmt, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		// Found a non-comment, non-empty line
		return false
	}
	return true
}

// IsClosed returns true if the store has been closed.
func (s *DoltStore) IsClosed() bool {
	return s.closed.Load()
}

// Close closes the database connection and removes any 0-byte noms LOCK files
// left behind by the embedded Dolt engine.
func (s *DoltStore) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.db != nil {
		if cerr := doltutil.CloseWithTimeout("db", s.db.Close); cerr != nil {
			// Timeout is non-fatal for cleanup - just log it
			if !errors.Is(cerr, context.Canceled) {
				err = errors.Join(err, cerr)
			}
		}
	}
	s.db = nil

	// Clean up 0-byte noms LOCK files. The Dolt engine creates these when
	// opening a database; they should be removed on clean shutdown but may
	// persist after crashes or when bd init triggers hook reopens.
	if s.dbPath != "" {
		cleanZeroByteNomsLocks(s.dbPath)
	}

	return err
}

// cleanZeroByteNomsLocks removes 0-byte noms LOCK files from all databases
// under doltDir. Only empty LOCK files are removed — non-empty ones may
// indicate an active lock held by a running server.
func cleanZeroByteNomsLocks(doltDir string) {
	entries, err := os.ReadDir(doltDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		lockPath := filepath.Join(doltDir, entry.Name(), ".dolt", "noms", "LOCK")
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			continue
		}
		if info.Size() == 0 {
			_ = os.Remove(lockPath)
		}
	}
}

// Path returns the database directory path
func (s *DoltStore) Path() string {
	return s.dbPath
}

// UnderlyingDB returns the underlying *sql.DB connection
func (s *DoltStore) UnderlyingDB() *sql.DB {
	return s.db
}

// =============================================================================
// Version Control Operations (Dolt-specific extensions)
// =============================================================================

func (s *DoltStore) commitAuthorString() string {
	return fmt.Sprintf("%s <%s>", s.committerName, s.committerEmail)
}

// Commit creates a Dolt commit with the given message
func (s *DoltStore) Commit(ctx context.Context, message string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.commit",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(s.doltSpanAttrs()...),
	)
	defer func() { endSpan(span, retErr) }()
	// NOTE: In SQL procedure mode, Dolt defaults author to the authenticated SQL user
	// (e.g. root@localhost). Always pass an explicit author for deterministic history.
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return fmt.Errorf("failed to commit: %w", err)
	}
	return nil
}

// CommitPending creates a single Dolt commit for all uncommitted changes in the working set.
// Returns (true, nil) if changes were committed, (false, nil) if there was nothing to commit,
// or (false, err) on failure. The commit message summarizes the accumulated changes by
// querying dolt_diff to count issue-level operations.
//
// This is the primary commit mechanism for batch mode, where multiple bd commands
// accumulate changes in the working set before committing at a logical boundary.
func (s *DoltStore) CommitPending(ctx context.Context, actor string) (bool, error) {
	// Check if there are any committable changes (excluding dolt_ignore'd tables
	// like wisp tables, which appear in dolt_status but can't be staged).
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	if count == 0 {
		return false, nil // Nothing to commit
	}

	msg := s.buildBatchCommitMessage(ctx, actor)
	if err := s.Commit(ctx, msg); err != nil {
		// Dolt may report "nothing to commit" even when Status() showed changes
		// (e.g., system tables or schema-only diffs). Treat as no-op.
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "nothing to commit") || strings.Contains(errLower, "no changes") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// buildBatchCommitMessage generates a descriptive commit message summarizing
// what changed since the last commit by querying dolt_diff against HEAD.
// It reports issue-level create/update/delete counts and lists any other
// tables (labels, comments, events, etc.) that have uncommitted changes.
func (s *DoltStore) buildBatchCommitMessage(ctx context.Context, actor string) string {
	if actor == "" {
		actor = s.committerName
	}

	// Count issue-level changes by diff type
	var added, modified, removed int
	rows, err := s.db.QueryContext(ctx, `
		SELECT diff_type, COUNT(*) as cnt
		FROM dolt_diff('HEAD', 'WORKING', 'issues')
		GROUP BY diff_type
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var diffType string
			var count int
			if scanErr := rows.Scan(&diffType, &count); scanErr == nil {
				switch diffType {
				case "added":
					added = count
				case "modified":
					modified = count
				case "removed":
					removed = count
				}
			}
		}
		if rowErr := rows.Err(); rowErr != nil {
			// Best effort — proceed with whatever counts we gathered
			_ = rowErr
		}
	}

	// Check which other tables have uncommitted changes beyond issues.
	// This surfaces label, comment, event, and dependency changes that
	// would otherwise produce a generic fallback message.
	var otherTables []string
	statusRows, statusErr := s.db.QueryContext(ctx, `
		SELECT table_name FROM dolt_status s
		WHERE table_name != 'issues'
		AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if statusErr == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var table string
			if scanErr := statusRows.Scan(&table); scanErr == nil {
				otherTables = append(otherTables, table)
			}
		}
		_ = statusRows.Err() // Best effort
	}

	// Build descriptive message
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d created", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", modified))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", removed))
	}

	if len(parts) == 0 && len(otherTables) == 0 {
		return fmt.Sprintf("bd: batch commit by %s", actor)
	}

	msg := fmt.Sprintf("bd: batch commit by %s", actor)
	if len(parts) > 0 {
		msg += " — " + strings.Join(parts, ", ")
	}
	if len(otherTables) > 0 {
		msg += fmt.Sprintf(" (+ %s)", strings.Join(otherTables, ", "))
	}
	return msg
}

// isSSHRemote checks whether the configured remote URL uses SSH transport.
// SSH remotes (git+ssh://, ssh://, git@host:path) require CLI-based push/pull
// because CALL DOLT_PUSH through the SQL server times out — the MySQL connection
// drops before the SSH transfer completes.
func (s *DoltStore) isSSHRemote(ctx context.Context) bool {
	// Check SQL remotes first
	remotes, err := s.ListRemotes(ctx)
	if err == nil {
		for _, r := range remotes {
			if r.Name == s.remote {
				return doltutil.IsSSHURL(r.URL)
			}
		}
	}
	// Fall back to CLI remotes (covers drift where remote exists only in filesystem)
	if s.dbPath != "" {
		if url := doltutil.FindCLIRemote(s.dbPath, s.remote); url != "" {
			return doltutil.IsSSHURL(url)
		}
	}
	return false
}

// mainRemoteCredentials returns credentials for the main remote, or nil if none.
func (s *DoltStore) mainRemoteCredentials() *remoteCredentials {
	if s.remoteUser == "" && s.remotePassword == "" {
		return nil
	}
	return &remoteCredentials{Username: s.remoteUser, Password: s.remotePassword}
}

// doltCLIPush shells out to `dolt push` from the database directory.
// Used for SSH remotes where CALL DOLT_PUSH times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only,
// avoiding process-wide env var races with concurrent goroutines.
func (s *DoltStore) doltCLIPush(ctx context.Context, force bool, creds *remoteCredentials) error {
	ctx, cancel := context.WithTimeout(ctx, cliExecTimeout)
	defer cancel()
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, s.remote, s.branch)
	cmd := exec.CommandContext(ctx, "dolt", args...) // #nosec G204 -- fixed command with validated remote/branch
	cmd.Dir = s.dbPath
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt push failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIPull shells out to `dolt pull` from the database directory.
// Used for SSH remotes where CALL DOLT_PULL times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only.
func (s *DoltStore) doltCLIPull(ctx context.Context, creds *remoteCredentials) error {
	ctx, cancel := context.WithTimeout(ctx, cliExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dolt", "pull", s.remote, s.branch) // #nosec G204 -- fixed command
	cmd.Dir = s.dbPath
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt pull failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Push pushes commits to the remote.
// For SSH remotes (including Hosted Dolt SSH), uses CLI `dolt push` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PUSH with --user authentication.
// For other remotes (DoltHub, S3, GCS, file), uses CALL DOLT_PUSH via SQL.
func (s *DoltStore) Push(ctx context.Context) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.push",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", s.remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.mainRemoteCredentials()
	// SSH remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env, avoiding
	// process-wide env var races with concurrent goroutines.
	if s.isSSHRemote(ctx) {
		return s.doltCLIPush(ctx, false, creds)
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
			if err != nil {
				return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
			}
			return nil
		})
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH(?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// ForcePush force-pushes commits to the remote, overwriting remote changes.
// Use when the remote has uncommitted changes in its working set.
// For SSH remotes (including Hosted Dolt SSH), uses CLI `dolt push --force` to avoid MySQL connection timeouts.
func (s *DoltStore) ForcePush(ctx context.Context) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.force_push",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", s.remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.mainRemoteCredentials()
	// SSH remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if s.isSSHRemote(ctx) {
		return s.doltCLIPush(ctx, true, creds)
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
			if err != nil {
				return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
			}
			return nil
		})
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--force', ?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// Pull pulls changes from the remote.
// Passes branch explicitly to avoid "did not specify a branch" errors.
// For SSH remotes (including Hosted Dolt SSH), uses CLI `dolt pull` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PULL with --user authentication.
func (s *DoltStore) Pull(ctx context.Context) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.pull",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", s.remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.mainRemoteCredentials()
	// SSH remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if s.isSSHRemote(ctx) {
		if err := s.doltCLIPull(ctx, creds); err != nil {
			return err
		}
		if err := s.resetAutoIncrements(ctx); err != nil {
			return fmt.Errorf("failed to reset auto-increments after pull: %w", err)
		}
		return nil
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			_, err := s.db.ExecContext(ctx, "CALL DOLT_PULL('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
			if err != nil {
				return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
			}
			if err := s.resetAutoIncrements(ctx); err != nil {
				return fmt.Errorf("failed to reset auto-increments after pull: %w", err)
			}
			return nil
		})
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PULL(?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
	}
	if err := s.resetAutoIncrements(ctx); err != nil {
		return fmt.Errorf("failed to reset auto-increments after pull: %w", err)
	}
	return nil
}

func (s *DoltStore) resetAutoIncrements(ctx context.Context) error {
	tables := []string{"events", "comments", "issue_snapshots", "compaction_snapshots", "wisp_events", "wisp_comments"}
	for _, table := range tables {
		var maxID int64
		//nolint:gosec // G201: table is a hardcoded constant
		err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COALESCE(MAX(id), 0) FROM %s", table)).Scan(&maxID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1146 {
				continue
			}
			return fmt.Errorf("failed to query max id for %s: %w", table, err)
		}
		if maxID > 0 {
			//nolint:gosec // G201: table is a hardcoded constant
			if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s AUTO_INCREMENT = %d", table, maxID+1)); err != nil {
				return fmt.Errorf("failed to reset AUTO_INCREMENT for %s: %w", table, err)
			}
		}
	}
	return nil
}

// Branch creates a new branch
func (s *DoltStore) Branch(ctx context.Context, name string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.branch",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.branch", name),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_BRANCH(?)", name); err != nil {
		return fmt.Errorf("failed to create branch %s: %w", name, err)
	}
	return nil
}

// Checkout switches to the specified branch
func (s *DoltStore) Checkout(ctx context.Context, branch string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.checkout",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.branch", branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		return fmt.Errorf("failed to checkout branch %s: %w", branch, err)
	}
	s.branch = branch
	return nil
}

// Merge merges the specified branch into the current branch.
// Returns any merge conflicts if present. Implements storage.VersionedStorage.
func (s *DoltStore) Merge(ctx context.Context, branch string) (conflicts []storage.Conflict, retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.merge",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.merge_branch", branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	// DOLT_MERGE may create a merge commit; pass explicit author for determinism.
	_, err := s.db.ExecContext(ctx, "CALL DOLT_MERGE('--author', ?, ?)", s.commitAuthorString(), branch)
	if err != nil {
		// Check if the error is due to conflicts
		mergeConflicts, conflictErr := s.GetConflicts(ctx)
		if conflictErr == nil && len(mergeConflicts) > 0 {
			span.SetAttributes(attribute.Int("dolt.conflicts", len(mergeConflicts)))
			return mergeConflicts, nil
		}
		retErr = fmt.Errorf("failed to merge branch %s: %w", branch, err)
		return nil, retErr
	}
	return nil, nil
}

// CurrentBranch returns the current branch name
func (s *DoltStore) CurrentBranch(ctx context.Context) (string, error) {
	var branch string
	err := s.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch)
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return branch, nil
}

// DeleteBranch deletes a branch (used to clean up import branches)
func (s *DoltStore) DeleteBranch(ctx context.Context, branch string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", branch)
	if err != nil {
		return fmt.Errorf("failed to delete branch %s: %w", branch, err)
	}
	return nil
}

// Log returns recent commit history
func (s *DoltStore) Log(ctx context.Context, limit int) ([]CommitInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT commit_hash, committer, email, date, message
		FROM dolt_log
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get log: %w", err)
	}
	defer rows.Close()

	var commits []CommitInfo
	for rows.Next() {
		var c CommitInfo
		if err := rows.Scan(&c.Hash, &c.Author, &c.Email, &c.Date, &c.Message); err != nil {
			return nil, fmt.Errorf("failed to scan commit: %w", err)
		}
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

// CommitInfo represents a Dolt commit
type CommitInfo struct {
	Hash    string
	Author  string
	Email   string
	Date    time.Time
	Message string
}

// HistoryEntry represents a row from dolt_history_* table
type HistoryEntry struct {
	CommitHash string
	Committer  string
	CommitDate time.Time
	// Issue data at that commit
	IssueData map[string]interface{}
}

// HasRemote checks if a Dolt remote with the given name exists.
func (s *DoltStore) HasRemote(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.queryRowContext(ctx, func(row *sql.Row) error {
		return row.Scan(&count)
	}, "SELECT COUNT(*) FROM dolt_remotes WHERE name = ?", name)
	if err != nil {
		return false, fmt.Errorf("failed to check remote %s: %w", name, err)
	}
	return count > 0, nil
}

// AddRemote adds a Dolt remote
func (s *DoltStore) AddRemote(ctx context.Context, name, url string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
	if err != nil {
		return fmt.Errorf("failed to add remote %s: %w", name, err)
	}
	return nil
}

// Status returns the current Dolt status (staged/unstaged changes)
func (s *DoltStore) Status(ctx context.Context) (*DoltStatus, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}
	defer rows.Close()

	status := &DoltStatus{
		Staged:   make([]StatusEntry, 0),
		Unstaged: make([]StatusEntry, 0),
	}

	for rows.Next() {
		var tableName string
		var staged bool
		var statusStr string
		if err := rows.Scan(&tableName, &staged, &statusStr); err != nil {
			return nil, fmt.Errorf("failed to scan status: %w", err)
		}
		entry := StatusEntry{Table: tableName, Status: statusStr}
		if staged {
			status.Staged = append(status.Staged, entry)
		} else {
			status.Unstaged = append(status.Unstaged, entry)
		}
	}
	return status, rows.Err()
}

// DoltStatus represents the current repository status
type DoltStatus struct {
	Staged   []StatusEntry
	Unstaged []StatusEntry
}

// StatusEntry represents a changed table
type StatusEntry struct {
	Table  string
	Status string // "new", "modified", "deleted"
}
