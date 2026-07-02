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
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
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
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// DefaultSQLPort is the default port for dolt sql-server.
const DefaultSQLPort = 3307

// testDatabasePrefixes are name prefixes that indicate a test database.
// Used by isTestDatabaseName to prevent test databases from being created
// on the production Dolt server (Clown Shows #12-#18).
var testDatabasePrefixes = []string{
	"testdb_",
	"beads_test",
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

// autoStartRefs tracks in-process reference counts for auto-started dolt
// sql-server processes, keyed by resolved server directory. When the count
// drops to zero, the server is stopped. This prevents test-started servers
// from leaking (GH#2542) while allowing multiple stores to share one server.
// Normal repo-local auto-starts are intentionally not tracked here: those
// servers should stay up like an explicit `bd dolt start`, rather than being
// torn down at the end of each command.
var autoStartRefs struct {
	mu sync.Mutex
	m  map[string]int
}

func autoStartAcquire(serverDir string) {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil {
		autoStartRefs.m = make(map[string]int)
	}
	autoStartRefs.m[serverDir]++
}

// autoStartAcquireExisting increments the refcount for serverDir only when the
// current process is already tracking that auto-started server. This lets later
// stores share the same test-owned server without taking ownership of servers
// started by other processes.
func autoStartAcquireExisting(serverDir string) bool {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil || autoStartRefs.m[serverDir] <= 0 {
		return false
	}
	autoStartRefs.m[serverDir]++
	return true
}

// autoStartRelease decrements the refcount for serverDir and stops the server
// when it reaches zero. Returns any error from stopping the server.
// If the server is already stopped (e.g. killed externally, or never started),
// the ErrServerNotRunning sentinel is silently absorbed to avoid false
// "failed to stop" warnings (GH#2670).
func autoStartRelease(serverDir string) error {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil {
		return nil
	}
	autoStartRefs.m[serverDir]--
	if autoStartRefs.m[serverDir] <= 0 {
		delete(autoStartRefs.m, serverDir)
		// Stop is idempotent: returns ErrServerNotRunning (possibly joined
		// with cleanup errors) when the server is already gone. Strip the
		// sentinel but propagate any real cleanup failures.
		return doltserver.IgnoreNotRunning(doltserver.Stop(serverDir))
	}
	return nil
}

// shouldStopAutoStartedServerOnClose reports whether an auto-started server
// should be treated as test-owned cleanup state instead of a normal repo-local
// server. In real repos, auto-start should behave like a persistent helper
// server, not a single-command subprocess.
func shouldStopAutoStartedServerOnClose(cfg *Config) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return true
	}
	return isTestDatabaseName(cfg.Database)
}

// Compile-time interface checks.
var _ storage.DoltStorage = (*DoltStore)(nil)
var _ storage.RawDBAccessor = (*DoltStore)(nil)
var _ storage.StoreLocator = (*DoltStore)(nil)
var _ storage.LifecycleManager = (*DoltStore)(nil)
var _ storage.PendingCommitter = (*DoltStore)(nil)
var _ storage.GarbageCollector = (*DoltStore)(nil)
var _ storage.Flattener = (*DoltStore)(nil)
var _ storage.Compactor = (*DoltStore)(nil)
var _ storage.SchemaMigrator = (*DoltStore)(nil)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db            *sql.DB
	dbPath        string       // Path to Dolt data directory (server root, e.g. .beads/dolt/)
	beadsDir      string       // Path to .beads directory (parent of dbPath)
	database      string       // Database name (subdirectory under dbPath)
	closed        atomic.Bool  // Tracks whether Close() has been called
	connStr       string       // Connection string for reconnection
	mu            sync.RWMutex // Protects concurrent access
	readOnly      bool         // True if opened in read-only mode
	credentialKey []byte       // Random encryption key for federation credentials

	customStatusDetailedCache []types.CustomStatus
	customStatusCache         []string
	customStatusCached        bool
	customTypeCache           []string
	customTypeCached          bool
	infraTypeCache            map[string]bool
	infraTypeCached           bool
	cacheMu                   sync.Mutex

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
	serverMode     bool   // true when connected to external dolt sql-server (not embedded)

	// autoStartedServerDir is set when this store triggered a dolt sql-server
	// auto-start. Close() uses it to stop the server when the last store
	// referencing it is closed (tracked via autoStartRefs).
	autoStartedServerDir string
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
	ServerSocket   string // Unix domain socket path (overrides Host/Port when set)
	ServerHost     string // Server host (default: 127.0.0.1)
	ServerPort     int    // Server port (default: 3307)
	ServerUser     string // MySQL user (default: root)
	ServerPassword string // MySQL password (default: empty, can be set via BEADS_DOLT_PASSWORD)
	ServerTLS      bool   // Enable TLS for server connections (required for Hosted Dolt)

	// Remote auth for Hosted Dolt push/pull (optional)
	// When set, Push/Pull use the --user flag and set DOLT_REMOTE_PASSWORD env var.
	RemoteUser     string // Hosted Dolt remote user (set via DOLT_REMOTE_USER env var)
	RemotePassword string // Hosted Dolt remote password (set via DOLT_REMOTE_PASSWORD env var)

	// SyncRemote holds the effective sync remote URL (from sync.remote
	// or deprecated sync.git-remote). Used for context-aware error hints.
	SyncRemote string

	// CreateIfMissing allows CREATE DATABASE when the target database does not
	// exist on the server. Only explicit initialization, migration, or new-board
	// creation paths should set this to true. Normal open paths leave it false,
	// which causes an error if the database is missing — preventing silent
	// creation of shadow databases on the wrong server.
	CreateIfMissing bool

	// ServerMode indicates this config targets an external dolt sql-server
	// rather than the embedded Dolt engine. Set by the store factory based
	// on metadata.json dolt_mode or BEADS_DOLT_SERVER_MODE env var.
	ServerMode bool

	// ProxiedServer indicates this config targets a per-workspace proxied
	// dolt sql-server (a parent proxy + a child dolt sql-server, both rooted
	// at <BeadsDir>/proxieddb). Mutually exclusive with ServerMode: the
	// proxied path owns its own connection details and does not consult
	// ServerHost/Port/Socket/User. Set by the store factory based on
	// metadata.json dolt_mode=proxied-server.
	ProxiedServer bool

	// AutoStart enables transparent server auto-start when connection fails.
	// When true and the host is localhost, bd will start a dolt sql-server
	// automatically if one isn't running. Disabled under orchestrator (GT_ROOT set).
	AutoStart bool

	// DisableAutoStart suppresses implicit server startup even when standalone
	// defaults would enable it. Diagnostic paths use this to stay read-only.
	DisableAutoStart bool

	// MaxOpenConns overrides the connection pool size (0 = default 10).
	// Set to 1 for branch isolation in tests (DOLT_CHECKOUT is session-level).
	MaxOpenConns int

	// MaxIdleConns overrides the maximum number of idle pooled connections
	// (0 = default min(5, MaxOpenConns)). Higher values keep more connections
	// warm between queries, reducing NewConnection/ConnectionClosed churn.
	MaxIdleConns int

	// ConnMaxLifetime overrides how long a pooled connection may be reused
	// before the pool retires it (0 = default 1 hour). Long-lived daemons
	// should not use a short lifetime — every retire+reopen shows up as a
	// NewConnection event in dolt-server.log and churns the pool for no
	// benefit when the server is local and stable.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime overrides how long a connection may sit idle in the pool
	// before the pool retires it (0 = default 20s). This must stay below the
	// dolt sql-server wait_timeout (currently 30s) so the pool retires an idle
	// connection before the server reaps it server-side; otherwise the next
	// query handed a server-reaped connection fails with "invalid connection".
	ConnMaxIdleTime time.Duration
}

// Defaults for the *sql.DB connection pool. Exported for tests/callers that
// want to reason about the out-of-the-box pool limits without having to read
// openServerConnection.
const (
	defaultMaxOpenConns    = 10
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = time.Hour
	// defaultConnMaxIdleTime keeps idle pooled connections shorter-lived than the
	// dolt sql-server wait_timeout (30s) so the pool retires an idle connection
	// before the server reaps it; this prevents the next read from picking up a
	// server-closed connection and failing with "invalid connection".
	defaultConnMaxIdleTime = 20 * time.Second
)

// cliExecTimeout is the maximum time to wait for dolt CLI push/pull operations.
// SSH transfers can hang indefinitely on network issues or SSH key prompts;
// this prevents the process from blocking forever.
const cliExecTimeout = 5 * time.Minute

func withCLIExecTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, cliExecTimeout)
}

// fsckTimeout is the maximum time to wait for dolt fsck to verify the local
// chunk store before a push. fsck reads local files only; 30 seconds is ample
// for any DB size we currently operate.
const fsckTimeout = 30 * time.Second

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
	if schema.IsMigrationLockError(err) {
		return true
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
	hint := lockProcessHint()
	return fmt.Errorf("%w\n\nThe Dolt database is locked.%s\n"+
		"Try: bd doctor --fix (clears stale locks), or kill the holding process.", err, hint)
}

// lockProcessHint tries to identify the process holding the database lock.
// Returns a hint string like " Process 12345 (bd) may be holding the lock."
// Returns empty string if identification fails or on unsupported platforms.
func lockProcessHint() string {
	// Look for other bd/dolt processes that might hold the lock
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc not available (macOS, Windows, FreeBSD) — skip PID detection
		return ""
	}

	myPID := os.Getpid()
	var holders []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == myPID {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		cmd := string(cmdline)
		if strings.Contains(cmd, "bd") || strings.Contains(cmd, "dolt") {
			holders = append(holders, fmt.Sprintf("%d", pid))
		}
	}

	if len(holders) == 0 {
		return ""
	}
	if len(holders) == 1 {
		return fmt.Sprintf(" Process %s (bd/dolt) may be holding the lock.", holders[0])
	}
	return fmt.Sprintf(" Processes %s (bd/dolt) may be holding the lock.", strings.Join(holders, ", "))
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
	retryCount          metric.Int64Counter
	lockWaitMs          metric.Float64Histogram
	circuitTrips        metric.Int64Counter
	circuitRejected     metric.Int64Counter
	serializationErrors metric.Int64Counter
	writeRetries        metric.Int64Counter
	connAcquireMs       metric.Float64Histogram
	poolWaitCount       metric.Int64Counter
	poolWaitMs          metric.Float64Histogram
}

func init() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	doltMetrics.retryCount, _ = m.Int64Counter("bd.db.retry_count",
		metric.WithDescription("SQL operations retried due to server-mode transient errors"),
		metric.WithUnit("{retry}"),
	)
	doltMetrics.lockWaitMs, _ = m.Float64Histogram("bd.db.lock_wait_ms",
		metric.WithDescription("Time spent waiting to acquire database locks"),
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
	doltMetrics.serializationErrors, _ = m.Int64Counter("bd.db.serialization_errors",
		metric.WithDescription("Serialization failures (MySQL 1213/1205) before retry"),
		metric.WithUnit("{error}"),
	)
	doltMetrics.writeRetries, _ = m.Int64Counter("bd.write_retries_total",
		metric.WithDescription("Write-tx retries in withRetryTx (label: type=serialization|connection)"),
		metric.WithUnit("{retry}"),
	)
	doltMetrics.connAcquireMs, _ = m.Float64Histogram("bd.db.conn_acquire_ms",
		metric.WithDescription("Time to acquire a pooled connection for a Dolt transaction"),
		metric.WithUnit("ms"),
	)
	doltMetrics.poolWaitCount, _ = m.Int64Counter("bd.db.pool_wait_count",
		metric.WithDescription("Number of times a connection acquisition had to wait for the pool"),
		metric.WithUnit("{wait}"),
	)
	doltMetrics.poolWaitMs, _ = m.Float64Histogram("bd.db.pool_wait_ms",
		metric.WithDescription("Total time connections spent waiting due to pool exhaustion"),
		metric.WithUnit("ms"),
	)
}

// registerPoolGauges registers observable gauges that report sql.DB pool stats
// on each OTel collection cycle. These are essential for diagnosing shared-server
// degradation under multi-worktree load (GH#3140).
func (s *DoltStore) registerPoolGauges() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	db := s.db

	m.Int64ObservableGauge("bd.db.pool_open", //nolint:errcheck,gosec
		metric.WithDescription("Current number of open connections (in-use + idle)"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().OpenConnections))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_in_use", //nolint:errcheck,gosec
		metric.WithDescription("Connections currently in use"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().InUse))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_idle", //nolint:errcheck,gosec
		metric.WithDescription("Idle connections in pool"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().Idle))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_max_open", //nolint:errcheck,gosec
		metric.WithDescription("Maximum number of open connections (pool limit)"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().MaxOpenConnections))
			return nil
		}),
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
			attribute.Bool("db.server_mode", true), // TODO: update when embedded mode returns
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

// withReadTx runs fn inside a transaction while holding the store's read-lock.
// Used for read operations that need a *sql.Tx to share issueops functions.
//
// The whole BeginTx+fn is wrapped in withRetry so a transient connection error
// (e.g. "invalid connection" when the dolt sql-server reaps a pooled connection
// that has been idle past its wait_timeout) is retried rather than surfaced to
// the caller. This is safe because fn is read-only and the transaction is always
// rolled back, so re-running the operation has no side effects.
func (s *DoltStore) withReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.withRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin read tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		return fn(tx)
	})
}

func (s *DoltStore) withRetryTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 5 * time.Second
	if s.serverMode {
		bo.MaxElapsedTime = 15 * time.Second
	}
	return backoff.Retry(func() error {
		err := s.withWriteTx(ctx, fn)
		if err == nil {
			return nil
		}
		// Serialization failures (1213/1205) guarantee a server-side rollback,
		// so the write never landed — safe to replay at any phase.
		if isSerializationError(err) {
			doltMetrics.serializationErrors.Add(ctx, 1)
			doltMetrics.writeRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "serialization")))
			return err // retryable
		}
		// Connection failures are only safe to replay BEFORE commit (BeginTx or
		// the body): nothing was committed. A failure tagged errCommitPhase is
		// ambiguous — the commit may have landed before the connection dropped —
		// so replaying could double-apply the write. Surface it instead.
		if isRetryableError(err) {
			if errors.Is(err, errCommitPhase) {
				return backoff.Permanent(fmt.Errorf("write commit result indeterminate after connection loss (not retried to avoid double-apply): %w", err))
			}
			doltMetrics.writeRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "connection")))
			return err // pre-commit transient: retryable
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
}

func (s *DoltStore) withWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		// Tag commit-phase failures so withRetryTx can tell an ambiguous commit
		// loss apart from a safe-to-replay pre-commit failure.
		return fmt.Errorf("commit write tx: %w (%w)", err, errCommitPhase)
	}
	return nil
}

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

// RemoteName returns the configured default sync remote name ("origin" unless
// overridden), the remote Push/Pull target when no explicit remote is given.
func (s *DoltStore) RemoteName() string {
	return s.remote
}

// BackupAdd registers a Dolt backup destination.
func (s *DoltStore) BackupAdd(ctx context.Context, name, url string) error {
	return versioncontrolops.BackupAdd(ctx, s.db, name, url)
}

// BackupSync pushes the database to the named backup destination.
func (s *DoltStore) BackupSync(ctx context.Context, name string) error {
	return versioncontrolops.BackupSync(ctx, s.db, name)
}

// BackupRemove removes a configured Dolt backup destination.
func (s *DoltStore) BackupRemove(ctx context.Context, name string) error {
	return versioncontrolops.BackupRemove(ctx, s.db, name)
}

// BackupDatabase registers dir as a file:// Dolt backup remote and syncs
// the full database to it, preserving complete commit history.
func (s *DoltStore) BackupDatabase(ctx context.Context, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup destination does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}
	backupName := "backup_export"

	// Register as a backup remote (idempotent — remove first if exists).
	_ = versioncontrolops.BackupRemove(ctx, s.db, backupName)
	if err := versioncontrolops.BackupAdd(ctx, s.db, backupName, backupURL); err != nil {
		// Another backup (e.g. "default" registered by `bd backup init`) may
		// already point to this URL. In that case, sync using the existing
		// remote name rather than failing.
		if conflict := versioncontrolops.ExtractAddressConflictName(err); conflict != "" {
			if syncErr := versioncontrolops.BackupSync(ctx, s.db, conflict); syncErr != nil {
				return fmt.Errorf("sync to backup: %w", syncErr)
			}
			return nil
		}
		return fmt.Errorf("register backup remote: %w", err)
	}
	if err := versioncontrolops.BackupSync(ctx, s.db, backupName); err != nil {
		return fmt.Errorf("sync to backup: %w", err)
	}
	return nil
}

// RestoreDatabase restores the database from a Dolt backup at dir.
// When force is true, an existing database is overwritten.
func (s *DoltStore) RestoreDatabase(ctx context.Context, dir string, force bool) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup source does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}
	return versioncontrolops.BackupRestore(ctx, s.db, backupURL, s.database, force)
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
		// Check env var first — this is the highest-priority override and
		// must be consulted even when no config file was loaded.
		if d := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); d != "" {
			cfg.Database = d
		} else if os.Getenv("BEADS_TEST_MODE") == "1" && cfg.Path != "" {
			// Test mode: derive unique database name from path for isolation.
			// Each test creates a unique temp directory, so hashing the path
			// gives each test its own database on the shared test server.
			h := fnv.New64a()
			_, _ = h.Write([]byte(cfg.Path)) // hash.Hash.Write never returns an error
			cfg.Database = fmt.Sprintf("testdb_%x", h.Sum64())
		} else {
			fmt.Fprintf(os.Stderr, "warning: no database name configured; falling back to default %q\n", configfile.DefaultDoltDatabase)
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

	// Server connection defaults (applied in server mode; embedded mode bypasses TCP)
	if cfg.ServerSocket == "" {
		cfg.ServerSocket = os.Getenv("BEADS_DOLT_SERVER_SOCKET")
	}
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
	// onto production even when the port env var is set to 3307 by the orchestrator's beads module.
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
	}
	// If env var didn't provide a port, consult the full resolution chain:
	// port file > config.yaml > metadata.json (GH#2590).
	// Resolve from the owning .beads dir when available; cfg.Path is the Dolt
	// data path, not the config directory, and using it directly can miss the
	// repo-local port file or metadata.
	if cfg.ServerPort == 0 {
		resolveDir := cfg.BeadsDir
		if resolveDir == "" && cfg.Path != "" {
			resolveDir = filepath.Dir(cfg.Path)
		}
		if resolveDir != "" {
			if resolved := doltserver.DefaultConfig(resolveDir); resolved.Port > 0 {
				cfg.ServerPort = resolved.Port
			}
		}
	}
	// Port 0 means "not yet resolved" — auto-start (EnsureRunning) will
	// allocate an ephemeral port. Don't default to 3307 as that caused
	// cross-project data leakage (GH#2098, GH#2372).
	//
	// Test mode guard: force port 1 (immediate fail) if we'd hit production
	// or have no port, to prevent test databases leaking onto production.
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
	// Clean stale circuit breaker files before checking — prevents leftover
	// state from previous sessions poisoning fresh inits (GH#2598).
	CleanStaleCircuitBreakerFiles()

	breaker := maybeNewCircuitBreaker(cfg.ServerHost, cfg.ServerPort, cfg.Database)

	// Circuit breaker: fail-fast if the server is known to be down.
	if breaker != nil && !breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return nil, ErrCircuitOpen
	}

	// Tracks server dir if we auto-started a server (for cleanup in Close, GH#2542).
	var autoStartedDir string
	trackAutoStartedServer := shouldStopAutoStartedServerOnClose(cfg)
	resolvedBeadsDir := cfg.BeadsDir
	if resolvedBeadsDir == "" {
		resolvedBeadsDir = filepath.Dir(cfg.Path) // fallback: cfg.Path is .beads/dolt → parent is .beads/
	}
	serverDir := doltserver.ResolveServerDir(resolvedBeadsDir)

	// Fail-fast connectivity check before MySQL protocol initialization.
	// This gives an immediate, clear error if the Dolt server isn't running,
	// rather than waiting for MySQL driver timeouts.
	var addr string
	var conn net.Conn
	var dialErr error
	if cfg.ServerSocket != "" {
		addr = cfg.ServerSocket
		conn, dialErr = net.DialTimeout("unix", cfg.ServerSocket, 500*time.Millisecond)
	} else {
		addr = net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
		conn, dialErr = net.DialTimeout("tcp", addr, 500*time.Millisecond)
	}
	if dialErr != nil {
		// Auto-start: if enabled and connecting locally via TCP, start a server.
		// Socket mode is excluded — auto-start creates a TCP listener, not a
		// unix socket, so the DSN would still fail. Socket users are expected
		// to manage their own server lifecycle.
		canAutoStart := cfg.AutoStart && cfg.Path != "" &&
			cfg.ServerSocket == "" && isLocalHost(cfg.ServerHost)
		if canAutoStart {
			port, startedByUs, startErr := doltserver.EnsureRunningDetailed(resolvedBeadsDir)
			if startErr != nil {
				return nil, fmt.Errorf("Dolt server unreachable at %s and auto-start failed: %w\n\n"+
					"To start manually: bd dolt start\n"+
					"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml",
					addr, startErr)
			}
			// Only tests should stop auto-started servers on Close(). In normal
			// repo-local server mode, leaving the server up avoids endpoint churn
			// and circuit-breaker trips between commands.
			if startedByUs && trackAutoStartedServer {
				autoStartedDir = serverDir
				autoStartAcquire(autoStartedDir)
			}
			// Update port — EnsureRunning allocates an ephemeral port
			if port != cfg.ServerPort {
				if cfg.ServerPort > 0 {
					fmt.Fprintf(os.Stderr, "Warning: Dolt server endpoint changed: port %d → %d (auto-start)\n", cfg.ServerPort, port)
					fmt.Fprintf(os.Stderr, "  Previous port was unreachable. If other tools expect port %d, they may see stale data.\n", cfg.ServerPort)
					fmt.Fprintf(os.Stderr, "  To pin a port: set dolt.port in .beads/config.yaml\n")
				}
				cfg.ServerPort = port
				addr = net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
				breaker = maybeNewCircuitBreaker(cfg.ServerHost, cfg.ServerPort, cfg.Database)
			}
			// Retry connection with longer timeout (server just started)
			conn, dialErr = net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr != nil {
				// Release auto-start ref on connection failure
				if autoStartedDir != "" {
					_ = autoStartRelease(autoStartedDir)
				}
				if breaker != nil {
					breaker.RecordFailure()
				}
				return nil, fmt.Errorf("Dolt server auto-started but still unreachable at %s: %w\n\n"+
					"Check logs: %s", addr, dialErr, doltserver.LogPath(resolvedBeadsDir))
			}
		} else {
			if breaker != nil {
				breaker.RecordFailure()
			}
			var hint string
			if cfg.ServerSocket != "" {
				hint = fmt.Sprintf("The Dolt server is not listening on socket %s.\n"+
					"Ensure the server is started with --socket:\n"+
					"  dolt sql-server --socket %s\n"+
					"Auto-start is not supported in socket mode.",
					cfg.ServerSocket, cfg.ServerSocket)
			} else if !cfg.AutoStart && doltserver.IsAutoStartDisabled() {
				hint = "Dolt server auto-start is disabled (dolt.auto-start: false).\n" +
					"Start the server manually:\n  bd dolt start"
			} else {
				hint = "The Dolt server may not be running. Try:\n  bd dolt start"
			}
			return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\n%s",
				addr, dialErr, hint)
		}
	}
	_ = conn.Close()

	// If this process already owns a test-started auto-start server, later
	// stores sharing it must participate in the refcount so one Close() does
	// not stop the server out from under another open store.
	if autoStartedDir == "" && trackAutoStartedServer && autoStartAcquireExisting(serverDir) {
		autoStartedDir = serverDir
	}

	// TCP dial succeeded — record success to reset the breaker
	if breaker != nil {
		breaker.RecordSuccess()
	}

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

	beadsDir := cfg.BeadsDir
	if beadsDir == "" && cfg.Path != "" {
		beadsDir = filepath.Dir(cfg.Path) // cfg.Path is .beads/dolt → parent is .beads/
	}

	store := &DoltStore{
		db:                   db,
		dbPath:               cfg.Path,
		beadsDir:             beadsDir,
		database:             cfg.Database,
		connStr:              connStr,
		breaker:              breaker,
		committerName:        cfg.CommitterName,
		committerEmail:       cfg.CommitterEmail,
		remote:               cfg.Remote,
		branch:               "main",
		remoteUser:           cfg.RemoteUser,
		remotePassword:       cfg.RemotePassword,
		serverMode:           true,
		readOnly:             cfg.ReadOnly,
		autoStartedServerDir: autoStartedDir,
	}

	// Forward-drift guard runs on read-only AND writable opens. A binary older
	// than the database's schema cannot migrate it forward: MigrateUp no-ops
	// when the DB is already past the binary's latest migration (atLatest uses
	// >=), so without this guard a writable open would proceed and later queries
	// would fail with cryptic unknown-column errors instead of a clear
	// "upgrade bd" message (the stale-binary incident behind #4135/#4137).
	if err := schema.CheckForwardDrift(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !cfg.ReadOnly {
		if err := store.initSchema(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	if !cfg.CreateIfMissing {
		var verifyErr error
		if cfg.Database == doltserver.GlobalDatabaseName {
			verifyErr = store.verifyGlobalProjectIdentity(ctx, cfg.BeadsDir)
		} else {
			verifyErr = store.verifyProjectIdentity(ctx, cfg.BeadsDir)
		}
		if verifyErr != nil {
			_ = db.Close()
			return nil, verifyErr
		}
	}

	if isLocalHost(cfg.ServerHost) && shouldPersistResolvedPortFile() {
		beadsDir := cfg.BeadsDir
		if beadsDir == "" && cfg.Path != "" {
			beadsDir = filepath.Dir(cfg.Path)
		}
		_ = doltserver.EnsurePortFile(beadsDir, cfg.ServerPort)
	}

	// All writers operate on main — transaction isolation via RunInTransaction
	// replaces the former branch-per-worker approach (BD_BRANCH).
	store.branch = "main"

	// Register observable pool gauges for diagnosing shared-server degradation (GH#3140).
	// These report sql.DB.Stats() on each OTel scrape — no-op when telemetry is off.
	store.registerPoolGauges()

	return store, nil
}

func shouldPersistResolvedPortFile() bool {
	return os.Getenv("BEADS_DOLT_SERVER_PORT") == "" && os.Getenv("BEADS_DOLT_PORT") == ""
}

// verifyProjectIdentity checks that the database belongs to the expected project.
// If both the local metadata.json and the database have a project_id, they must match.
// Returns nil if verification passes or is not applicable (missing IDs = old setup).
func (s *DoltStore) verifyProjectIdentity(ctx context.Context, beadsDir string) error {
	if beadsDir == "" {
		return nil // can't verify without knowing beadsDir
	}

	// Load local project ID from metadata.json
	metaCfg, err := configfile.Load(beadsDir)
	if err != nil || metaCfg == nil {
		return nil // no local config — skip verification
	}
	localID := metaCfg.ProjectID
	if localID == "" {
		return nil // old-style metadata.json without project_id — skip
	}

	// Read project ID from database metadata table
	dbID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || dbID == "" {
		return nil // old database without project_id — skip
	}

	if localID != dbID {
		return fmt.Errorf(
			"PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Local project ID (metadata.json):  %s\n"+
				"  Database project ID:               %s\n\n"+
				"This means the Dolt server is serving a DIFFERENT project's database.\n"+
				"This can happen when:\n"+
				"  - Another project's server is running on the same port\n"+
				"  - The server restarted with a different data directory\n\n"+
				"To diagnose: bd dolt status\n"+
				"Do NOT run 'bd init' — your data likely exists, just on a different server.",
			localID, dbID)
	}
	return nil
}

func (s *DoltStore) verifyGlobalProjectIdentity(ctx context.Context, beadsDir string) error {
	if beadsDir == "" {
		return nil
	}

	metaCfg, err := configfile.Load(beadsDir)
	if err != nil || metaCfg == nil {
		return nil
	}
	expectedID := metaCfg.GlobalProjectID
	if expectedID == "" {
		return nil
	}

	dbID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || dbID == "" {
		return nil
	}

	if expectedID != dbID {
		return fmt.Errorf(
			"GLOBAL PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Expected global project ID (metadata.json): %s\n"+
				"  Database project ID:                        %s\n\n"+
				expectedID, dbID)
	}
	return nil
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
// Adds ReadTimeout/WriteTimeout for long-lived connection pools.
func buildServerDSN(cfg *Config, database string) string {
	base := doltutil.ServerDSN{
		Socket:   cfg.ServerSocket,
		Host:     cfg.ServerHost,
		Port:     cfg.ServerPort,
		User:     cfg.ServerUser,
		Password: cfg.ServerPassword,
		Database: database,
		TLS:      cfg.ServerTLS,
	}
	// Parse the base DSN and add pool-specific timeouts.
	parsed, err := mysql.ParseDSN(base.String())
	if err != nil {
		return base.String()
	}
	parsed.ReadTimeout = 10 * time.Second
	parsed.WriteTimeout = 10 * time.Second
	return parsed.FormatDSN()
}

// execWithLongTimeout opens a one-shot database connection with readTimeout=5m
// and executes the given query. Push/pull operations can exceed the default
// readTimeout when the server performs network I/O to git remotes.
//
// The query is wrapped in an explicit transaction (BEGIN/COMMIT) so that
// DOLT_PULL merge operations succeed even when the server runs with
// autocommit=1. Without this, Dolt rejects merges under autocommit because
// it cannot expose conflict-resolution tables to the caller.
func (s *DoltStore) execWithLongTimeout(ctx context.Context, query string, args ...any) error {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// execWithLongTimeoutNoTx executes a long-running Dolt stored procedure without
// an explicit transaction. Push operations do not need the pull/merge conflict
// handling above, and DOLT_PUSH has diverged from direct `dolt push` behavior
// when wrapped in a SQL transaction.
func (s *DoltStore) execWithLongTimeoutNoTx(ctx context.Context, query string, args ...any) error {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, query, args...)
	return err
}

// applyPoolLimits configures the pool on db using the sensible-default
// connection pool limits, overridden by any non-zero Config fields.
//
// These limits are deliberately oriented at long-lived daemons: a 1h
// connection lifetime lets the same physical MySQL connection be reused
// for thousands of queries, so dolt-server.log no longer shows a
// NewConnection/ConnectionClosed pair every few queries.
func applyPoolLimits(db *sql.DB, cfg *Config) {
	maxOpen := defaultMaxOpenConns
	if cfg.MaxOpenConns > 0 {
		maxOpen = cfg.MaxOpenConns
	}

	maxIdle := defaultMaxIdleConns
	if cfg.MaxIdleConns > 0 {
		maxIdle = cfg.MaxIdleConns
	}
	// MaxIdleConns must never exceed MaxOpenConns or database/sql silently
	// clamps it and we end up with a different pool shape than requested.
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	lifetime := defaultConnMaxLifetime
	if cfg.ConnMaxLifetime > 0 {
		lifetime = cfg.ConnMaxLifetime
	}

	idle := defaultConnMaxIdleTime
	if cfg.ConnMaxIdleTime > 0 {
		idle = cfg.ConnMaxIdleTime
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)
	db.SetConnMaxIdleTime(idle)
}

// openServerConnection opens a connection to a dolt sql-server via MySQL protocol
func openServerConnection(ctx context.Context, cfg *Config) (*sql.DB, string, error) {
	connStr := buildServerDSN(cfg, cfg.Database)

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open Dolt server connection: %w", err)
	}

	// Configure the pool. *sql.DB is safe for concurrent use and manages its
	// own pool — the same Store reuses these connections across every query
	// for the lifetime of the daemon, rather than opening a fresh one each
	// time (which used to show up as endless NewConnection/ConnectionClosed
	// pairs in dolt-server.log).
	applyPoolLimits(db, cfg)

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
			return nil, "", databaseNotFoundError(cfg)
		}

		_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database)) //nolint:gosec // G201: cfg.Database validated by ValidateDatabaseName above
		if err != nil {
			// Dolt may return error 1007 even with IF NOT EXISTS - ignore if database already exists
			errLower := strings.ToLower(err.Error())
			if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
				_ = db.Close()
				// Check for connection refused - server likely not running
				if strings.Contains(errLower, "connection refused") || strings.Contains(errLower, "connect: connection refused") {
					return nil, "", fmt.Errorf("failed to connect to Dolt server at %s:%d: %w\n\nThe Dolt server may not be running. Try:\n  bd dolt start    # Start a local server\n  gt dolt start    # If using an orchestrator",
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

// initSchemaOnDB applies pending schema migrations. schema.MigrateUp tracks
// applied versions in schema_migrations and backfills legacy config-driven
// tables. Returns the number of migrations applied.
func initSchemaOnDB(ctx context.Context, db *sql.DB) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("schema: pin connection: %w", err)
	}
	defer conn.Close()

	var dbName string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName); err != nil {
		return 0, fmt.Errorf("schema: read database name: %w", err)
	}

	applied, err := schema.MigrateUpWithLock(ctx, conn, dbName)
	if err != nil {
		return applied, fmt.Errorf("schema migration: %w", err)
	}
	return applied, nil
}

func initSchemaOnDBWithRetry(ctx context.Context, db *sql.DB) (int, error) {
	return initSchemaOnDBWithRetryAndGate(ctx, db, nil)
}

// initSchemaOnDBWithRetryAndGate is initSchemaOnDBWithRetry with an optional
// pre-migration gate run INSIDE the retry loop. The gate's own reads
// (schema_migrations, dolt_remotes) can hit the same transient Dolt
// startup/catalog races the migration retry absorbs, so gate probe errors are
// retried with them instead of failing the open fast (bd-6dnrw.30); a
// *schema.RemoteMigrateGateError refusal stays permanent.
func initSchemaOnDBWithRetryAndGate(ctx context.Context, db *sql.DB, gate func(context.Context, *sql.DB) error) (int, error) {
	// Schema initialization for server mode is idempotent. Retry transient
	// Dolt startup/catalog races and contended migration-lock attempts so
	// concurrent bd processes converge instead of failing one unlucky waiter.
	schemaBO := backoff.NewExponentialBackOff()
	schemaBO.InitialInterval = 100 * time.Millisecond
	// Must exceed schema.MigrateUpWithLock's 5s GET_LOCK wait so a contended
	// schema migration can time out once and still retry.
	schemaBO.MaxElapsedTime = serverRetryMaxElapsed
	var applied int
	err := backoff.Retry(func() error {
		if gate != nil {
			if gateErr := gate(ctx, db); gateErr != nil {
				if !schema.IsRemoteMigrateGateError(gateErr) && isRetryableError(gateErr) {
					return gateErr
				}
				return backoff.Permanent(gateErr)
			}
		}
		var schemaErr error
		applied, schemaErr = initSchemaOnDB(ctx, db)
		if schemaErr != nil && isRetryableError(schemaErr) {
			return schemaErr
		}
		if schemaErr != nil {
			return backoff.Permanent(schemaErr)
		}
		return nil
	}, backoff.WithContext(schemaBO, ctx))
	return applied, err
}

func (s *DoltStore) initSchema(ctx context.Context) error {
	// Schema migrations can run arbitrarily long (e.g. full-table recomputes
	// such as the is_blocked backfill in migration 0047). The main connection
	// pool sets a 10s ReadTimeout (see buildServerDSN); a slow migration over
	// that pool aborts mid-flight with "i/o timeout" and leaves tables dirty,
	// which then blocks every subsequent migration attempt. Run the migration
	// pass over a dedicated connection with no read/write timeout. Cancellation
	// is governed by the caller's context, not a fixed deadline.
	migDB, err := s.openMigrationDB()
	if err != nil {
		return err
	}
	defer migDB.Close()
	// #4259: refuse to silently apply pending migrations to a remote-backed,
	// already-initialized database — that is how two clones fork the schema.
	// The gate runs inside the retry loop, before each migration attempt: its
	// reads can hit transient startup/catalog races (retryable) while a gate
	// refusal is permanent and never retried into a migration.
	// Use the on-disk fallback: a freshly (auto-)started server can report an
	// empty dolt_remotes table even though remotes are persisted in .dolt/config
	// (GH#2315), so an SQL-only check would miss the remote on the first write
	// open after an upgrade.
	gate := func(ctx context.Context, db *sql.DB) error {
		return schema.CheckRemoteMigrateGateForRemoteWithRemoteCheck(ctx, db, s.remote, s.hasPersistedCLIRemote)
	}
	_, err = initSchemaOnDBWithRetryAndGate(ctx, migDB, gate)
	return err
}

// ApplySchemaMigrations runs idempotent schema migrations under the
// per-database advisory lock, with retry for transient lock contention.
// Implements storage.SchemaMigrator.
func (s *DoltStore) ApplySchemaMigrations(ctx context.Context) (int, error) {
	migDB, err := s.openMigrationDB()
	if err != nil {
		return 0, err
	}
	defer migDB.Close()
	return initSchemaOnDBWithRetry(ctx, migDB)
}

// openMigrationDB opens a one-off connection pool for schema migrations with no
// read/write timeout. Migrations may run far longer than the default 10s pool
// timeout, and timing out part-way leaves the database in a dirty, half-migrated
// state. The single connection is closed by the caller once migration completes.
func (s *DoltStore) openMigrationDB() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN for migration connection: %w", err)
	}
	cfg.ReadTimeout = 0
	cfg.WriteTimeout = 0
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open migration connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
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

	// Stop auto-started server when the last store referencing it closes.
	if s.autoStartedServerDir != "" {
		if stopErr := autoStartRelease(s.autoStartedServerDir); stopErr != nil {
			// Best-effort: don't mask other errors
			fmt.Fprintf(os.Stderr, "Warning: failed to stop auto-started dolt server: %v\n", stopErr)
		}
		s.autoStartedServerDir = ""
	}

	// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
	// directory — including noms/LOCK files. These are Dolt-internal files.
	// Removing them WILL cause unrecoverable data corruption and data loss.
	// Dolt manages these files itself; external interference is never safe.

	return err
}

// Path returns the database directory path
func (s *DoltStore) Path() string {
	return s.dbPath
}

// IsReadOnly reports whether the store was opened in read-only mode. It is a
// test-facing accessor: production read-only enforcement comes from the
// read-only open mode itself (the readOnly field guards every write path), not
// from callers consulting this method. Tests such as
// TestDepRoutedTargetOpensReadOnly use it to assert that routed
// dependency/link target resolution opens a by-ID target read-only, so
// resolving it never opens a foreign project writable or runs open-time
// migrations into its history (bd-6dnrw.32, GH#3231).
func (s *DoltStore) IsReadOnly() bool {
	return s.readOnly
}

// CLIDir returns the directory for dolt CLI operations (push/pull/remote/fetch).
// The actual database lives in a subdirectory of Path() named after the database.
// Use this instead of Path() when running dolt CLI commands that target the
// actual database (e.g., remote add/remove, push, pull).
func (s *DoltStore) CLIDir() string {
	if s.serverMode && doltserver.IsSharedServerMode() && s.beadsDir != "" {
		return filepath.Join(doltserver.ResolveDoltDir(s.beadsDir), s.database)
	}
	if s.dbPath == "" {
		return ""
	}
	return filepath.Join(s.dbPath, s.database)
}

// DoltGC runs Dolt garbage collection to reclaim disk space.
// Pins a single connection to avoid session state loss on pooled *sql.DB.
func (s *DoltStore) DoltGC(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for gc: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.DoltGC(ctx, conn)
}

// Flatten squashes all Dolt commit history into a single commit.
// Pins a single connection because the stored procedures (DOLT_CHECKOUT,
// DOLT_RESET, etc.) rely on session-scoped state that would be lost if
// steps execute on different pooled connections.
func (s *DoltStore) Flatten(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for flatten: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.Flatten(ctx, conn)
}

// Compact squashes old Dolt commits while preserving recent ones.
// Pins a single connection for session-scoped stored procedures.
func (s *DoltStore) Compact(ctx context.Context, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for compact: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.Compact(ctx, conn, initialHash, boundaryHash, oldCommits, recentHashes)
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

// configCommitMode controls how commitWorkingSet treats the config table, which
// holds both internal keys (issue_prefix) and synced user data (kv.* keys,
// including kv.memory.* persistent memories).
type configCommitMode int

const (
	// configExclude skips config entirely (GH#2455): a plain Commit must not
	// sweep a concurrent writer's half-applied issue_prefix change into an
	// unrelated commit.
	configExclude configCommitMode = iota
	// configIncludeUserKVOnly stages config for the pre-pull auto-commit, but
	// only when every dirty config row is this clone's own user KV data (the
	// kv.* namespace, which includes kv.memory.* memories). Any other dirty
	// config key — an internal key such as issue_prefix above all — aborts the
	// commit with operator guidance so the pull never auto-commits unsafe
	// config (GH#2455 + GH#2474).
	configIncludeUserKVOnly
	// configIncludeAll stages every dirty config row. Used only to conclude a
	// merge whose conflicts the operator resolved explicitly (bd federation
	// sync --strategy): that resolution is intentional, so a resolved
	// issue_prefix (or any config row) must be committed, not dropped.
	configIncludeAll
)

// Commit creates a Dolt commit with the given message.
//
// GH#2455: Stages all dirty tables EXCEPT config, then commits with '-m'.
// The old '-Am' approach staged ALL dirty tables including config, which
// swept up stale issue_prefix changes from concurrent operations. By
// excluding config from automatic staging, we prevent the corruption.
//
// Callers that intentionally modify config (e.g., CommitPending after
// 'bd config set') must call CommitWithConfig instead.
func (s *DoltStore) Commit(ctx context.Context, message string) error {
	return s.commitWorkingSet(ctx, message, configExclude)
}

// commitBeforePull commits the working set ahead of a pull's merge, INCLUDING
// config. The pre-pull auto-commit (GH#2474) must include config because user
// KV data lives there as kv.* rows (persistent memories are the kv.memory.*
// subset) and Commit() deliberately skips config (GH#2455): without this those
// rows sit permanently uncommitted, so the "clean the working set before
// merging" step leaves config dirty and DOLT_MERGE refuses to start ("cannot
// merge with uncommitted changes").
//
// It includes ONLY this clone's own user kv.* rows: if any other config key is
// dirty (an internal key such as issue_prefix above all) it refuses rather than
// auto-committing it, so the stale-config corruption GH#2455 guards against is
// never re-opened by a pull. Auto-*resolution* of a config conflict stays
// narrower still — only convergent kv.memory.* keys (see
// configConflictsAreMemoryConvergent) — so widening the commit screen to the
// whole kv. namespace cannot auto-resolve a genuine kv.* conflict; it only stops
// generic `bd kv set` writes from wedging the pull. Config is staged explicitly
// (via DOLT_ADD in commitWorkingSet) rather than through CommitWithConfig's
// DOLT_COMMIT('-Am'), which was observed not to stage config reliably under the
// server-mode stored-procedure path. Committing this clone's own kv.* rows as the
// merge basis is the same explicit, user-initiated action CommitPending ('bd dolt
// commit') already performs, so it does not widen the concurrent-writer race
// GH#2455 guards against.
func (s *DoltStore) commitBeforePull(ctx context.Context, message string) error {
	return s.commitWorkingSet(ctx, message, configIncludeUserKVOnly)
}

// CommitMergeResolution concludes a merge whose conflicts were resolved by an
// explicit operator strategy (bd federation sync --strategy / bd vc merge
// --strategy ours|theirs), committing the resolved working set INCLUDING config.
// Plain Commit excludes config (GH#2455), so a config-only resolution — exactly
// the case this change makes routine by syncing kv.* through config — would be
// silently dropped, leaving the merge unconcluded and re-wedging the next
// pull/sync. Unlike commitBeforePull it does not screen config keys: the operator
// chose this resolution, so whichever config rows it touched (issue_prefix
// included) are committed as-is. It satisfies storage.VersionControl so cmd/bd
// concludes bd vc merge --strategy through the same config-inclusive commit
// instead of the config-excluding Commit that would drop the resolution.
func (s *DoltStore) CommitMergeResolution(ctx context.Context, message string) error {
	return s.commitWorkingSet(ctx, message, configIncludeAll)
}

// commitWorkingSet stages the dirty tables reported by dolt_status and commits
// them with '-m'. The config table is staged according to mode: configExclude
// skips it (GH#2455) so a concurrent writer's half-applied issue_prefix change
// is never swept into an unrelated commit; configIncludeUserKVOnly stages it for
// the pre-pull path but refuses when any non-kv. (internal) config key is dirty;
// configIncludeAll stages every dirty config row to conclude an explicit merge
// resolution.
func (s *DoltStore) commitWorkingSet(ctx context.Context, message string, mode configCommitMode) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.commit",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(s.doltSpanAttrs()...),
	)
	defer func() { endSpan(span, retErr) }()

	// Pin a single connection so all operations run on the same Dolt session.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	// GH#2455: stage each dirty table individually, skipping config unless the
	// mode opts it in, to avoid sweeping up stale issue_prefix changes from
	// concurrent operations. Query dolt_status first.
	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		// If dolt_status fails, fall back to nothing (rare edge case).
		return fmt.Errorf("failed to query dolt_status: %w", err)
	}
	var tables []string
	configDirty := false
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan dolt_status: %w", err)
		}
		if table == "config" {
			configDirty = true
			if mode == configExclude {
				continue
			}
		}
		tables = append(tables, table)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dolt_status: %w", err)
	}

	// GH#2455 + GH#2474: the pre-pull auto-commit includes config so user kv.*
	// writes sync, but it must NOT auto-commit any internal (non-kv.) config key.
	// Refuse before staging anything so the merge is never concluded over an
	// unsafe config row; the operator commits those explicitly.
	if configDirty && mode == configIncludeUserKVOnly {
		if err := s.assertDirtyConfigUserKVOnly(ctx, conn); err != nil {
			return err
		}
	}

	if len(tables) == 0 {
		return nil // Nothing to commit (all changes were config-only or dolt_ignore'd)
	}

	for _, table := range tables {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			// config when the mode intentionally includes it is the whole reason
			// we stage here: silently skipping a failed DOLT_ADD('config') would
			// leave config dirty and re-wedge the merge, so surface it instead.
			if table == "config" && mode != configExclude {
				return fmt.Errorf("failed to stage config before commit: %w", err)
			}
			// Best effort: some tables may be dolt_ignore'd (e.g., wisps).
			// DOLT_ADD fails for ignored tables; skip silently.
			continue
		}
	}

	// NOTE: In SQL procedure mode, Dolt defaults author to the authenticated SQL user
	// (e.g. root@localhost). Always pass an explicit author for deterministic history.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}

// assertDirtyConfigUserKVOnly returns an error unless every config row dirty in
// the working set is this clone's own user KV data (the kv.* namespace, which
// includes kv.memory.* memories). The pre-pull auto-commit opts config into the
// staged set so user KV writes sync and stop wedging DOLT_MERGE (GH#2474), but
// auto-committing an unrelated dirty internal config key such as issue_prefix
// would re-open the GH#2455 stale-config corruption — that is the operator's
// explicit `bd dolt commit` to make, not the pull's. Screening on the whole kv.
// namespace (not just kv.memory.*) un-wedges generic `bd kv set` writes too: a
// kv.* row is this clone's own data, exactly as safe to auto-commit as a memory,
// and a genuine kv.* merge conflict is still left for the operator because
// auto-resolution stays kv.memory.*-only (configConflictsAreMemoryConvergent).
// config's primary key is `key`, so dolt_diff exposes to_key/from_key; an add or
// delete leaves one side NULL, so COALESCE picks whichever key the change carries.
func (s *DoltStore) assertDirtyConfigUserKVOnly(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT COALESCE(to_key, from_key) FROM dolt_diff('HEAD', 'WORKING', 'config')")
	if err != nil {
		return fmt.Errorf("inspect dirty config before pull: %w", err)
	}
	defer rows.Close()

	var unsafe []string
	for rows.Next() {
		var key sql.NullString
		if err := rows.Scan(&key); err != nil {
			return fmt.Errorf("scan dirty config key: %w", err)
		}
		if key.Valid && !strings.HasPrefix(key.String, kvkeys.Prefix) {
			unsafe = append(unsafe, key.String)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate dirty config diff: %w", err)
	}
	if len(unsafe) > 0 {
		return fmt.Errorf("refusing to auto-commit %d dirty internal config key(s) before pull: %s; "+
			"only user %s* keys auto-commit before a pull (GH#2455) — commit or revert "+
			"these explicitly with `bd dolt commit` first", len(unsafe), strings.Join(unsafe, ", "), kvkeys.Prefix)
	}
	return nil
}

// CommitWithConfig creates a Dolt commit that includes the config table.
// Use this instead of Commit when the caller intentionally modified config
// (e.g., CommitPending after 'bd config set', 'bd init', or 'bd rename-prefix').
// GH#2455: Commit() excludes config to prevent sweeping up stale changes.
func (s *DoltStore) CommitWithConfig(ctx context.Context, message string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return fmt.Errorf("failed to commit: %w", err)
	}
	return nil
}

// doltAddAndCommit stages the specified tables and commits on a pinned
// connection. This prevents DOLT_COMMIT('-Am') from sweeping up stale
// working set changes from concurrent operations (GH#2455).
func (s *DoltStore) doltAddAndCommit(ctx context.Context, tables []string, commitMsg string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	for _, table := range tables {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
		commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit: %w", err)
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
	// GH#2455: CommitPending is an explicit user action (bd dolt commit) that
	// should include ALL pending changes, including config. Use CommitWithConfig
	// instead of Commit to ensure intentional config changes are committed.
	if err := s.CommitWithConfig(ctx, msg); err != nil {
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

// hasMatchingCLIRemote reports whether the local CLI directory contains the
// same remote URL that SQL reports. CLI push/pull/fetch run from CLIDir, so
// SQL visibility alone is not enough to route safely.
func (s *DoltStore) hasMatchingCLIRemote(remote, expectedURL string) bool {
	if expectedURL == "" {
		return false
	}
	cliDir := s.CLIDir()
	if cliDir == "" {
		return false
	}
	if !s.hasCLIDatabase() {
		return false
	}
	return doltutil.RemoteURLsMatch(doltutil.FindCLIRemote(cliDir, remote), expectedURL)
}

// hasCLIDatabase reports whether CLIDir points at an initialized Dolt database.
// SQL-capable routes use this as a CLI availability check and fall back to SQL
// when an external-server client has only a placeholder local directory.
func (s *DoltStore) hasCLIDatabase() bool {
	cliDir := s.CLIDir()
	if cliDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(cliDir, ".dolt"))
	return err == nil && info.IsDir()
}

// ensureMatchingCLIRemote materializes the local CLI remote needed before
// subprocess push/pull/fetch routing. SQL remains the source of truth; the CLI
// remote is only the local transport surface that dolt subprocesses read.
func (s *DoltStore) ensureMatchingCLIRemote(remote, expectedURL string) error {
	if s.hasMatchingCLIRemote(remote, expectedURL) {
		return nil
	}
	cliDir := s.CLIDir()
	if expectedURL == "" {
		return fmt.Errorf("remote %q has an empty SQL URL", remote)
	}
	if cliDir == "" {
		return fmt.Errorf("remote %q (%s) requires CLI routing but no CLI directory is configured", remote, expectedURL)
	}
	if err := doltutil.EnsureCLIRemote(cliDir, remote, expectedURL); err != nil {
		return fmt.Errorf("materialize CLI remote %q (%s) in %s: %w", remote, expectedURL, cliDir, err)
	}
	if !s.hasMatchingCLIRemote(remote, expectedURL) {
		return fmt.Errorf("materialized CLI remote %q in %s, but its URL does not match SQL URL %q", remote, cliDir, expectedURL)
	}
	return nil
}

func (s *DoltStore) prepareDoltCLITransfer(ctx context.Context, remote string, creds *remoteCredentials, args ...string) (*exec.Cmd, context.CancelFunc) {
	return prepareDoltCLITransferCommand(ctx, s.CLIDir(), creds, s.isS3Remote(ctx, remote), args...)
}

func prepareDoltCLITransferCommand(ctx context.Context, cliDir string, creds *remoteCredentials, s3Remote bool, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := withCLIExecTimeout(ctx)
	cmd := exec.CommandContext(ctx, "dolt", args...) // #nosec G204 -- fixed command with validated remote/ref args
	cmd.Dir = cliDir
	creds.applyToCmd(cmd)
	if s3Remote {
		applyS3ChecksumEnvToCmd(cmd)
	}
	return cmd, cancel
}

// prepareCLIRouteForGitProtocol reports whether the SQL-visible remote uses
// git wire protocol and prepares the matching local CLI remote before routing.
func (s *DoltStore) prepareCLIRouteForGitProtocol(ctx context.Context, remote string) (bool, error) {
	if s.CLIDir() == "" {
		return false, nil
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before git-protocol routing: %w", err)
	}
	for _, r := range remotes {
		if r.Name == remote {
			if !doltutil.IsGitProtocolURL(r.URL) {
				return false, nil
			}
			if err := s.ensureMatchingCLIRemote(remote, r.URL); err != nil {
				return false, fmt.Errorf("remote %q uses git protocol and requires CLI routing: %w", remote, err)
			}
			return true, nil
		}
	}
	return false, nil
}

// shouldUseCLIForGitProtocol is a compatibility wrapper for tests and older
// call sites. Prefer prepareCLIRouteForGitProtocol so mutation is explicit.
func (s *DoltStore) shouldUseCLIForGitProtocol(ctx context.Context, remote string) (bool, error) {
	return s.prepareCLIRouteForGitProtocol(ctx, remote)
}

// isGitProtocolRemote reports whether the SQL-visible remote uses git wire
// protocol and the same remote is available in the local CLI directory.
func (s *DoltStore) isGitProtocolRemote(ctx context.Context, remote string) bool {
	ok, err := s.prepareCLIRouteForGitProtocol(ctx, remote)
	if err != nil {
		log.Printf("warning: %v", err)
		return false
	}
	return ok
}

// mainRemoteCredentials returns credentials for the main remote, or nil if none.
func (s *DoltStore) mainRemoteCredentials() *remoteCredentials {
	if s.remoteUser == "" && s.remotePassword == "" {
		return nil
	}
	return &remoteCredentials{username: s.remoteUser, password: s.remotePassword}
}

// credentialsForRemote returns credentials only when the target remote is the
// default remote (s.remote). Non-default remotes get nil creds to avoid sending
// the wrong credentials to the wrong host.
func (s *DoltStore) credentialsForRemote(remote string) *remoteCredentials {
	if remote == s.remote {
		return s.mainRemoteCredentials()
	}
	return nil
}

// prePushFSCK runs dolt fsck --quiet to verify local chunk integrity before
// pushing. This prevents propagating Dolt remote corruption (dangling blob
// references) that arise when concurrent pushes race on the remote manifest.
//
// When multiple agents push simultaneously, one push's manifest update can
// land before another's chunks finish uploading, leaving a manifest that
// references chunks that were never stored. Any agent that then fetches and
// re-pushes that remote faithfully propagates the dangling reference.
//
// If CLIDir is empty or .dolt/noms does not exist, the check is skipped.
// Any fsck failure returns ErrDanglingReference — the push is NOT attempted.
func (s *DoltStore) prePushFSCK(ctx context.Context) error {
	dir := s.CLIDir()
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".dolt", "noms")); os.IsNotExist(err) {
		return nil
	}
	fsckCtx, cancel := context.WithTimeout(ctx, fsckTimeout)
	defer cancel()
	cmd := exec.CommandContext(fsckCtx, "dolt", "fsck", "--quiet") // #nosec G204 -- fixed command
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		output := strings.TrimSpace(string(out))
		// Distinguish "fsck couldn't run the integrity check" (environmental /
		// tooling issue) from "fsck ran and found integrity problems" (the actual
		// concern of PR #3447). Wrapping an open-failure as ErrDanglingReference
		// misleads users into thinking their db is corrupt.
		//
		// Concrete example: dolthub/dolt#10915 (Windows url.Parse bug, pre-v1.86.4)
		// caused fsck to construct a malformed file path and fail to open; users
		// running `bd dolt push` saw "dangling chunk reference" errors on perfectly
		// healthy databases.
		//
		// The two known "couldn't open" signatures from dolt are covered below.
		// Any other fsck failure still aborts the push so real dangling references
		// continue to block propagation.
		if fsckCouldNotOpen(output) {
			log.Printf("pre-push fsck could not run, skipping integrity check: %s", output)
			return nil
		}
		return fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s",
			ErrDanglingReference, output)
	}
	return nil
}

// fsckCouldNotOpen reports whether dolt fsck output indicates the check
// could not run at all (as opposed to finding integrity problems). Matches
// the known error phrasings dolt emits before any integrity work begins.
func fsckCouldNotOpen(output string) bool {
	switch {
	case strings.Contains(output, "Could not open dolt database"):
		return true
	case strings.Contains(output, "repository state is invalid"):
		return true
	default:
		return false
	}
}

// doltCLIPush shells out to `dolt push` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PUSH times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only,
// avoiding process-wide env var races with concurrent goroutines.
func (s *DoltStore) doltCLIPush(ctx context.Context, remote string, force bool, creds *remoteCredentials) error {
	if err := s.prePushFSCK(ctx); err != nil {
		return err
	}
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote, s.branch)
	cmd, cancel := s.prepareDoltCLITransfer(ctx, remote, creds, args...)
	defer cancel()
	applyNoGitHooksToCmd(cmd) // GH#3724
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt push failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIPull shells out to `dolt pull` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PULL times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only.
func (s *DoltStore) doltCLIPull(ctx context.Context, remote string, creds *remoteCredentials) error {
	cmd, cancel := s.prepareDoltCLITransfer(ctx, remote, creds, "pull", remote, s.branch)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt pull failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Push pushes commits to the remote.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt push` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PUSH with --user authentication.
// For other remotes (DoltHub, S3, GCS, file), uses CALL DOLT_PUSH via SQL.
func (s *DoltStore) Push(ctx context.Context) (retErr error) {
	return s.pushToRemote(ctx, s.remote, false)
}

// ForcePush force-pushes commits to the remote, overwriting remote changes.
// Use when the remote has uncommitted changes in its working set.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt push --force` to avoid MySQL connection timeouts.
func (s *DoltStore) ForcePush(ctx context.Context) (retErr error) {
	return s.pushToRemote(ctx, s.remote, true)
}

// PushRemote pushes commits to a named remote. Unlike Push(), which always
// uses the configured default remote (s.remote), PushRemote targets an
// explicit remote name. Credentials are only applied when the target remote
// matches the default remote; otherwise nil creds are used.
func (s *DoltStore) PushRemote(ctx context.Context, remote string, force bool) error {
	return s.pushToRemote(ctx, remote, force)
}

// pushToRemote is the internal implementation for all push operations.
// It routes through CLI or SQL based on the remote's protocol and credentials.
func (s *DoltStore) pushToRemote(ctx context.Context, remote string, force bool) (retErr error) {
	spanName := "dolt.push"
	if force {
		spanName = "dolt.force_push"
	}
	ctx, span := doltTracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.credentialsForRemote(remote)
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env, avoiding
	// process-wide env var races with concurrent goroutines.
	if useCLI, err := s.prepareCLIRouteForGitProtocol(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	// Credential CLI routing: when credentials are set and server is external,
	// route through CLI subprocess so credentials reach the dolt process via
	// cmd.Env (applyToCmd). The SQL path's withEnvCredentials sets process-wide
	// env vars that an external server cannot see.
	if useCLI, err := s.prepareCLIRouteForCredentials(ctx, remote, creds); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	// Cloud auth CLI routing: when cloud storage env vars (AZURE_*, AWS_*,
	// etc.) are set and we're in server mode, route through CLI so the dolt
	// subprocess inherits the current env. The SQL server may not have these
	// vars if it was started in a different context (GH#6).
	if useCLI, err := s.prepareCLIRouteForCloudAuth(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	if useCLI, err := s.shouldUseCLIForLocalRemoteWithError(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	if s.remoteUser != "" && remote == s.remote {
		return withRemoteOperationEnv(creds, s.isS3Remote(ctx, remote), func() error {
			if force {
				if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", s.remoteUser, remote, s.branch); err != nil {
					return fmt.Errorf("failed to force push to %s/%s: %w", remote, s.branch, err)
				}
			} else {
				if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", s.remoteUser, remote, s.branch); err != nil {
					return fmt.Errorf("failed to push to %s/%s: %w", remote, s.branch, err)
				}
			}
			return nil
		})
	}
	return withRemoteOperationEnv(nil, s.isS3Remote(ctx, remote), func() error {
		if force {
			if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--force', ?, ?)", remote, s.branch); err != nil {
				return fmt.Errorf("failed to force push to %s/%s: %w", remote, s.branch, err)
			}
		} else {
			if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", remote, s.branch); err != nil {
				return fmt.Errorf("failed to push to %s/%s: %w", remote, s.branch, err)
			}
		}
		return nil
	})
}

// Pull pulls changes from the remote.
// Passes branch explicitly to avoid "did not specify a branch" errors.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt pull` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PULL with --user authentication.
//
// If the pull results in merge conflicts on the metadata table only (e.g., from
// stale dolt_auto_push_* rows on multi-machine setups), the conflicts are
// automatically resolved using "theirs" strategy (GH#2466).
func (s *DoltStore) Pull(ctx context.Context) (retErr error) {
	return s.pullFromRemote(ctx, s.remote)
}

// PullRemote pulls changes from a named remote. Unlike Pull(), which always
// uses the configured default remote (s.remote), PullRemote targets an
// explicit remote name. Credentials are only applied when the target remote
// matches the default remote; otherwise nil creds are used.
func (s *DoltStore) PullRemote(ctx context.Context, remote string) error {
	return s.pullFromRemote(ctx, remote)
}

// pullFromRemote is the internal implementation for all pull operations.
// It routes through CLI or SQL based on the remote's protocol and credentials.
func (s *DoltStore) pullFromRemote(ctx context.Context, remote string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.pull",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	// GH#2474: Auto-commit pending changes before pull to prevent
	// "cannot merge with uncommitted changes" errors. Store initialization
	// (schema init, molecule loading, metadata writes) can dirty the working
	// set before the user's pull command runs.
	if !s.readOnly {
		if err := s.commitBeforePull(ctx, "auto-commit before pull"); err != nil {
			// "nothing to commit" is fine — working set is already clean
			if !isDoltNothingToCommit(err) {
				return fmt.Errorf("failed to commit pending changes before pull: %w", err)
			}
		}
	}

	// bd-6dnrw.3: capture the pre-pull HEAD so a successful merge can recompute
	// the denormalized is_blocked column for the rows it changed. Read before
	// the transport; an unreadable HEAD degrades to a full recompute.
	preHead := ""
	if !s.readOnly {
		if h, err := s.GetCurrentCommit(ctx); err == nil {
			preHead = h
		}
	}

	if err := s.pullTransport(ctx, remote); err != nil {
		return err
	}

	if !s.readOnly {
		if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
			return fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
		}
	}
	return nil
}

// pullTransport routes one pull through CLI or SQL based on the remote's
// protocol and credentials, including the post-pull conflict auto-resolution
// each route carries. Split from pullFromRemote so every successful route
// funnels back through the is_blocked recompute.
func (s *DoltStore) pullTransport(ctx context.Context, remote string) error {
	creds := s.credentialsForRemote(remote)
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if useCLI, err := s.prepareCLIRouteForGitProtocol(ctx, remote); err != nil {
		return err
	} else if useCLI {
		// CLI pull leaves any conflicts in the working set; run the auto-resolver so
		// git-protocol remotes get the same audit-only dependency / metadata repair
		// as the SQL DOLT_PULL path (#4259).
		return s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Credential CLI routing: mirrors git-protocol path, including post-pull
	// auto-resolution.
	if useCLI, err := s.prepareCLIRouteForCredentials(ctx, remote, creds); err != nil {
		return err
	} else if useCLI {
		return s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Cloud auth CLI routing (GH#6), including post-pull auto-resolution.
	if useCLI, err := s.prepareCLIRouteForCloudAuth(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Local file:// pulls intentionally stay on the SQL path. The matching CLI
	// guard is a push-only optimization; SQL pull keeps pullWithAutoResolve in
	// charge of metadata-only conflict repair.
	if s.remoteUser != "" && remote == s.remote {
		return withRemoteOperationEnv(creds, s.isS3Remote(ctx, remote), func() error {
			if err := s.pullWithAutoResolve(ctx, remote, "CALL DOLT_PULL('--user', ?, ?, ?)", s.remoteUser, remote, s.branch); err != nil {
				return fmt.Errorf("failed to pull from %s/%s: %w", remote, s.branch, err)
			}
			return nil
		})
	}
	return withRemoteOperationEnv(nil, s.isS3Remote(ctx, remote), func() error {
		if err := s.pullWithAutoResolve(ctx, remote, "CALL DOLT_PULL(?, ?)", remote, s.branch); err != nil {
			return fmt.Errorf("failed to pull from %s/%s: %w", remote, s.branch, err)
		}
		return nil
	})
}

// pullWithAutoResolve executes a DOLT_PULL query with long timeout and auto-resolves
// metadata-only merge conflicts using "theirs" strategy. This handles the common case
// where machine-local metadata rows (e.g., dolt_auto_push_*) diverge across clones
// and cause recurring merge conflicts on pull (GH#2466).
//
// Dolt may report merge conflicts in two ways:
//  1. DOLT_PULL itself returns an error (under autocommit)
//  2. DOLT_PULL succeeds but tx.Commit() fails (conflicts in working set)
//
// This method handles both by checking for conflicts after the pull call
// (whether it errored or not) and auto-resolving metadata-only conflicts.
// openLongTimeoutConn opens a dedicated single-connection *sql.DB to this store's
// database with a long read timeout, for merge/pull/conflict operations that can run
// longer than the default connection timeout. The caller must Close the returned DB.
func (s *DoltStore) openLongTimeoutConn() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// remote names the remote the query pulls from; the GH#3144 fetch+merge
// fallback targets it directly, so pulls from non-default remotes (PullRemote,
// federation peers) no longer fall back to s.remote.
func (s *DoltStore) pullWithAutoResolve(ctx context.Context, remote string, query string, args ...any) error {
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Allow commits with conflicts so we can inspect and resolve them.
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to set dolt_allow_commit_conflicts: %w", err)
	}
	// bd-6dnrw.4: a merge that violates a foreign key (e.g. one clone deleted
	// an issue while another inserted a child row referencing it) rolls the
	// whole transaction back before it can be inspected. Let it land in the
	// working set instead so tryRepairFKCascadeViolations can apply the
	// cascade semantics; the violation check before tx.Commit() below refuses
	// to commit anything the repair did not fully clear.
	if _, err := tx.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to set dolt_force_transaction_commit: %w", err)
	}

	_, pullErr := tx.ExecContext(ctx, query, args...)

	// GH#3144: When DOLT_PULL fails because upstream branch tracking is not
	// configured in repo_state.json (common when remote was added via
	// bd dolt remote add rather than bd bootstrap/dolt clone), fall back to
	// DOLT_FETCH + DOLT_MERGE which does not require tracking config.
	if pullErr != nil && isBranchTrackingError(pullErr) {
		if _, err := tx.ExecContext(ctx, "CALL DOLT_FETCH(?, ?)", remote, s.branch); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("fetch from %s/%s: %w", remote, s.branch, err)
		}
		trackingRef := remote + "/" + s.branch
		_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", trackingRef)
		if mergeErr != nil && strings.Contains(mergeErr.Error(), "up to date") {
			mergeErr = nil
		}
		pullErr = mergeErr
	}

	return s.settleMergeInTx(ctx, tx, pullErr)
}

// settleMergeInTx finishes a pull/merge that ran in tx: it auto-resolves the
// safe conflict classes, repairs FK cascade violations (bd-6dnrw.4), and
// commits — or rolls back when anything needs the operator. pullErr is the
// pull/merge statement's own error; it is surfaced whenever nothing was
// resolved or repaired. The tx must have been opened with
// dolt_allow_commit_conflicts and dolt_force_transaction_commit set, which is
// why the violation gate here is mandatory: with the force flag on, committing
// without it would persist a violated working set.
func (s *DoltStore) settleMergeInTx(ctx context.Context, tx *sql.Tx, pullErr error) error {
	// Check for merge conflicts regardless of whether DOLT_PULL errored.
	// Some Dolt versions error on conflicts, others leave them in the working set.
	resolved, resolveErr := s.tryAutoResolveMergeConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}

	// bd-578h9.15: conflicts the resolver declined are the operator's. Capture
	// them BEFORE the rollback wipes merge state — a post-rollback GetConflicts
	// on a fresh transaction sees an empty set, which made PullFrom's
	// conflict-reporting contract dead code on the SQL route. The resolver
	// pre-screens every table before resolving any, so a declined resolve
	// leaves dolt_conflicts fully intact here.
	if !resolved {
		if conflicts, cErr := versioncontrolops.GetConflicts(ctx, tx); cErr == nil && len(conflicts) > 0 {
			_ = tx.Rollback()
			return &versioncontrolops.MergeConflictsError{Conflicts: conflicts, MergeErr: pullErr}
		}
	}

	// bd-6dnrw.4: repair FK cascade violations the merge produced (child rows
	// whose parent issue was deleted on the other clone). Unrepaired
	// violations MUST NOT be committed.
	repairedViol, hadViol, violErr := s.tryRepairFKCascadeViolations(ctx, tx)
	if violErr != nil {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return violErr
	}
	if hadViol && !repairedViol {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return fmt.Errorf("pull merge left constraint violations bd cannot auto-repair; inspect dolt_constraint_violations and resolve before retrying")
	}

	if pullErr != nil && !resolved && !repairedViol {
		// Pull failed for a non-conflict reason, or conflicts include non-metadata tables.
		_ = tx.Rollback()
		return pullErr
	}

	// Conclude the merge for resolved conflicts only now, after the FK repair:
	// DOLT_COMMIT refuses a violated working set, so a merge carrying both
	// classes could never settle when the resolver committed first (bd-578h9.14).
	if resolved {
		if err := versioncontrolops.CommitResolvedConflicts(ctx, tx); err != nil {
			_ = tx.Rollback()
			if pullErr != nil {
				return pullErr
			}
			return err
		}
	}

	return tx.Commit()
}

// recomputeBlockedAfterPull recomputes the denormalized is_blocked column for
// the rows a pull's merge changed (bd-6dnrw.3) and commits the result.
// is_blocked is otherwise maintained only by local write paths, so a merge
// that brings in another clone's status or dependency changes leaves it stale
// and `bd ready` trusts it. fromCommit is the pre-pull HEAD; empty means it
// could not be read, which degrades to a full recompute. A pull that merged
// nothing (HEAD unchanged) is a no-op.
func (s *DoltStore) recomputeBlockedAfterPull(ctx context.Context, fromCommit string) error {
	if err := s.recomputeBlockedTx(ctx, fromCommit); err != nil {
		// The merge this recompute covers is already committed, so a plain
		// retry on the next pull would skip as "nothing merged" — leave a
		// marker so it widens its window instead (bd-578h9.11). Best-effort:
		// the recompute error is what matters.
		s.markBlockedRecomputePending(ctx, fromCommit)
		return err
	}
	// Derived state converges: every clone computes the same values from the
	// same merged graph, so committing is merge-safe. Commit no-ops when the
	// recompute changed nothing.
	if err := s.Commit(ctx, "bd: recompute is_blocked after pull"); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("commit is_blocked recompute: %w", err)
	}
	return nil
}

// RecomputeAllBlocked recomputes is_blocked for every issue and wisp in one full
// pass and returns the number of rows it corrected. It is the mode-independent
// repair behind 'bd recompute-blocked' and 'bd doctor --fix' (bd-6dnrw.37): the
// scoped post-pull recompute is skipped when a re-pull merges nothing, so a
// recompute that failed after its merge committed — or a conflicted pull the
// operator resolved by hand — leaves is_blocked stale until this full pass runs.
// Idempotent: a consistent database corrects nothing.
func (s *DoltStore) RecomputeAllBlocked(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin is_blocked recompute: %w", err)
	}
	// Refuse to derive and commit is_blocked from a dirty graph: the recompute
	// reads the current working set and stages only `issues`, so pre-existing
	// dirty issue/dependency edits would otherwise be swept into — or silently
	// inform — the repair commit (bd-6dnrw.37). Checked inside this tx so it
	// sees the same working set the recompute will read.
	if err := issueops.GuardBlockedRecomputeWorkingSet(ctx, tx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit is_blocked recompute: %w", err)
	}
	if changed > 0 {
		// Stage only issues — the synced table is_blocked lives on (wisps are
		// dolt_ignore'd) — so an unrelated dirty working set is not swept in.
		if err := s.doltAddAndCommit(ctx, []string{"issues"}, "bd: recompute is_blocked (full)"); err != nil {
			return int(changed), err
		}
	}
	return int(changed), nil
}

// recomputeBlockedTx runs the post-merge is_blocked recompute in its own
// transaction.
func (s *DoltStore) recomputeBlockedTx(ctx context.Context, fromCommit string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin is_blocked recompute: %w", err)
	}
	if err := issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, fromCommit); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit is_blocked recompute: %w", err)
	}
	return nil
}

// markBlockedRecomputePending best-effort records a failed post-merge
// is_blocked recompute (bd-578h9.11); see
// issueops.MarkIsBlockedRecomputePendingInTx.
func (s *DoltStore) markBlockedRecomputePending(ctx context.Context, fromCommit string) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	if err := issueops.MarkIsBlockedRecomputePendingInTx(ctx, tx, fromCommit); err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

// finishCLIPull runs the merge-conflict auto-resolver after a CLI-based pull
// (git-protocol, credentialed, or cloud-auth remotes). CLI `dolt pull` writes any
// merge conflicts into the shared working set but, unlike the SQL DOLT_PULL path,
// returns without a transaction we can inspect — so these remotes historically
// skipped the resolver entirely. With deterministic dependency ids (#4259) a
// same-edge conflict that differs only in audit columns is safe to auto-resolve, and
// the git remote topology in #4259 is exactly this CLI path; route it through the
// same resolver as the SQL path. pullErr is what doltCLIPull returned: a pull that
// fails *because* of conflicts is recoverable once they resolve, so we inspect the
// working set regardless and only surface pullErr when nothing was resolved.
func (s *DoltStore) finishCLIPull(ctx context.Context, pullErr error) error {
	if s.readOnly {
		// A read-only store cannot resolve or commit; surface the pull result as-is.
		return pullErr
	}
	resolved, resolveErr := s.autoResolveConflictsAfterCLIPull(ctx)
	if resolveErr != nil {
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}
	if pullErr != nil && !resolved {
		// Pull failed for a non-conflict reason, or conflicts are not auto-resolvable;
		// leave them in the working set for the operator.
		return pullErr
	}
	return nil
}

// autoResolveConflictsAfterCLIPull inspects the working set and auto-resolves the
// conflict classes that are safe without operator input (#4259 audit-only dependency
// edges, GH#2466 metadata). It runs on a connection from the store pool (s.db) on
// purpose: those connections are on the same branch the CLI `dolt pull` merged into,
// whereas a separately opened connection would default to the base branch and never
// see the conflicts. The pull's
// network transfer already completed in the subprocess, so no long-timeout connection
// is needed for the local resolve. Returns (true, nil) only if all conflicts were
// resolved and committed; (false, nil) when there is nothing to resolve or a conflict
// needs the operator, leaving the working set untouched for manual resolution.
func (s *DoltStore) autoResolveConflictsAfterCLIPull(ctx context.Context) (bool, error) {
	// Pin a single connection: @@dolt_allow_commit_conflicts is session-scoped,
	// and setting it through a pooled transaction leaks it to whichever caller
	// drains that connection next. Reset it before releasing the connection; if
	// the reset cannot run, discard the connection rather than return it dirty.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to acquire connection: %w", err)
	}
	varSet := false
	defer func() {
		if varSet {
			if _, err := conn.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 0"); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		_ = conn.Close()
	}()
	// Allow committing while conflicts exist so we can inspect and resolve them.
	if _, err := conn.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		return false, fmt.Errorf("failed to set dolt_allow_commit_conflicts: %w", err)
	}
	varSet = true
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	resolved, err := s.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	// bd-6dnrw.4: a CLI pull can also leave FK cascade violations in the
	// shared working set (child rows whose parent issue was deleted on the
	// other clone). Repair them like the SQL route does; unrepaired
	// violations roll back untouched for the operator.
	repairedViol, hadViol, violErr := s.tryRepairFKCascadeViolations(ctx, tx)
	if violErr != nil {
		_ = tx.Rollback()
		return false, violErr
	}
	if hadViol && !repairedViol {
		_ = tx.Rollback()
		return false, nil
	}
	if !resolved && !repairedViol {
		_ = tx.Rollback()
		return false, nil
	}
	// Conclude the merge for resolved conflicts only now, after the FK repair:
	// DOLT_COMMIT refuses a violated working set, so a merge carrying both
	// classes could never settle when the resolver committed first (bd-578h9.14).
	if resolved {
		if err := versioncontrolops.CommitResolvedConflicts(ctx, tx); err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit resolved conflicts: %w", err)
	}
	return true, nil
}

// tryAutoResolveMergeConflicts auto-resolves merge conflicts that are safe to
// resolve without operator input (GH#2466 metadata, #4259 audit-only
// dependency edges, bd-6dnrw.29 schema_migrations vintage rows, GH#2474
// convergent kv.memory.* config rows), returning
// (true, nil) only if ALL conflicts were resolved. The implementation is
// shared with the embedded pull path (bd-6dnrw.40); see
// versioncontrolops.TryAutoResolveMergeConflicts for the full contract.
func (s *DoltStore) tryAutoResolveMergeConflicts(ctx context.Context, tx *sql.Tx) (bool, error) {
	return versioncontrolops.TryAutoResolveMergeConflicts(ctx, tx)
}

// tryRepairFKCascadeViolations repairs the post-merge foreign-key constraint
// violations produced by the delete-vs-insert cascade hazard (bd-6dnrw.4).
// The caller's transaction must run with @@dolt_force_transaction_commit=1
// for the merge to survive long enough to be repaired, and must NOT commit
// when (repaired=false, had=true) — unrepaired violations are the operator's.
// The implementation is shared with the embedded pull path (bd-6dnrw.40); see
// versioncontrolops.TryRepairFKCascadeViolations for the full contract.
func (s *DoltStore) tryRepairFKCascadeViolations(ctx context.Context, tx *sql.Tx) (repaired, had bool, err error) {
	return versioncontrolops.TryRepairFKCascadeViolations(ctx, tx)
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
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for branch: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.CreateBranch(ctx, conn, name)
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
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for checkout: %w", err)
	}
	defer conn.Close()
	if err := versioncontrolops.CheckoutBranch(ctx, conn, branch); err != nil {
		return err
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

	// bd-578h9.11: like every pull path, a branch merge brings in writes that
	// bypassed the local is_blocked hooks; recompute after a conflict-free
	// merge. Conflicted merges defer to the caller's post-resolution hook
	// (Sync, bd vc merge --strategy) — recomputing over unresolved rows would
	// read garbage.
	preHead := ""
	if !s.readOnly {
		if h, err := s.GetCurrentCommit(ctx); err == nil {
			preHead = h
		}
	}

	conflicts, err := versioncontrolops.Merge(ctx, s.db, branch, s.commitAuthorString())
	if len(conflicts) > 0 {
		span.SetAttributes(attribute.Int("dolt.conflicts", len(conflicts)))
	}
	if err == nil && len(conflicts) == 0 && !s.readOnly {
		if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
			return conflicts, fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", rerr)
		}
	}
	return conflicts, err
}

// RecomputeBlockedAfterMerge recomputes the denormalized is_blocked column
// for the rows changed since fromCommit and commits the result — the hook a
// caller that resolved merge conflicts itself must run after committing the
// resolution (bd-578h9.11): conflicted merges skip the automatic recompute
// because unresolved rows would feed it garbage, and nothing else covers the
// merged-in writes. fromCommit is the pre-merge HEAD; empty degrades to a
// full-graph recompute.
func (s *DoltStore) RecomputeBlockedAfterMerge(ctx context.Context, fromCommit string) error {
	return s.recomputeBlockedAfterPull(ctx, fromCommit)
}

// CurrentBranch returns the current branch name
func (s *DoltStore) CurrentBranch(ctx context.Context) (string, error) {
	return versioncontrolops.CurrentBranch(ctx, s.db)
}

// DeleteBranch deletes a branch (used to clean up import branches)
func (s *DoltStore) DeleteBranch(ctx context.Context, branch string) error {
	return versioncontrolops.DeleteBranch(ctx, s.db, branch)
}

// Log returns recent commit history
func (s *DoltStore) Log(ctx context.Context, limit int) ([]CommitInfo, error) {
	return versioncontrolops.Log(ctx, s.db, limit)
}

// CommitInfo is an alias for storage.CommitInfo.
type CommitInfo = storage.CommitInfo

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
	return versioncontrolops.Status(ctx, s.db)
}

// DoltStatus is an alias for storage.Status.
type DoltStatus = storage.Status

// StatusEntry is an alias for storage.StatusEntry.
type StatusEntry = storage.StatusEntry
