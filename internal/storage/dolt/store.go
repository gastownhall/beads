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

	// Per-invocation caches (lifetime = DoltStore lifetime)
	customStatusDetailedCache    []types.CustomStatus // cached result of GetCustomStatusesDetailed
	customStatusCache            []string             // cached name-only result (derived from detailed)
	customStatusCached           bool                 // true once cache has been populated
	customTypeCache              []string             // cached result of GetCustomTypes
	customTypeCached             bool                 // true once customTypeCache has been populated
	infraTypeCache               map[string]bool      // cached result of GetInfraTypes
	infraTypeCached              bool                 // true once infraTypeCache has been populated
	blockedIDsCache              []string             // cached result of computeBlockedIDs
	blockedIDsCacheMap           map[string]bool
	blockedIDsCached             bool // true once blockedIDsCache has been populated
	blockedIDsCacheIncludesWisps bool // true if cache was computed with wisps
	cacheMu                      sync.Mutex

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
	ServerHost     string // Server host (default: 127.0.0.1)
	ServerPort     int    // Server port (default: 3307)
	ServerUser     string // MySQL user (default: root)
	ServerPassword string // MySQL password (default: empty, can be set via BEADS_DOLT_PASSWORD)
	ServerTLS      bool   // Enable TLS for server connections (required for Hosted Dolt)

	// Remote auth for Hosted Dolt push/pull (optional)
	// When set, Push/Pull use the --user flag and set DOLT_REMOTE_PASSWORD env var.
	RemoteUser     string // Hosted Dolt remote user (set via DOLT_REMOTE_USER env var)
	RemotePassword string // Hosted Dolt remote password (set via DOLT_REMOTE_PASSWORD env var)

	// SyncGitRemote holds the sync.git-remote config value (if any).
	// Used to provide context-aware hints in "database not found" errors.
	SyncGitRemote string

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

	// AutoStart enables transparent server auto-start when connection fails.
	// When true and the host is localhost, bd will start a dolt sql-server
	// automatically if one isn't running. Disabled under orchestrator (GT_ROOT set).
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
func (s *DoltStore) withReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// withRetryTx wraps withWriteTx with retry logic for serialization failures
// (MySQL 1213 deadlock, 1205 lock wait timeout). These errors guarantee the
// transaction was rolled back, so retrying is always safe.
//
// Connection-level errors (broken pipe, bad connection) are NOT retried here
// because they can occur after a successful commit, making retry unsafe for
// non-idempotent operations. Callers that need connection-level retry should
// use withRetry at a higher layer.
func (s *DoltStore) withRetryTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 5 * time.Second
	return backoff.Retry(func() error {
		err := s.withWriteTx(ctx, fn)
		if err != nil && isSerializationError(err) {
			return err // retryable
		}
		if err != nil {
			return backoff.Permanent(err)
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

// withWriteTx runs fn inside a transaction, committing on success.
// Used for write operations that delegate SQL work to issueops functions.
// The caller's fn should NOT call tx.Commit — withWriteTx handles that.
func (s *DoltStore) withWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
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

	breaker := maybeNewCircuitBreaker(cfg.ServerHost, cfg.ServerPort)

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

	// Fail-fast TCP check before MySQL protocol initialization.
	// This gives an immediate, clear error if the Dolt server isn't running,
	// rather than waiting for MySQL driver timeouts.
	addr := net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
	conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dialErr != nil {
		// Auto-start: if enabled and connecting to localhost, start a server
		if cfg.AutoStart && isLocalHost(cfg.ServerHost) && cfg.Path != "" {
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
				breaker = maybeNewCircuitBreaker(cfg.ServerHost, cfg.ServerPort)
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
			return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\nThe Dolt server may not be running. Try:\n  bd dolt start",
				addr, dialErr)
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

	// Project identity verification: detect cross-project data leakage (GH#2372).
	// If the local metadata.json has a project_id and the database has one too,
	// they must match. A mismatch means this client is connected to the wrong
	// project's Dolt server — refuse to proceed.
	if !cfg.CreateIfMissing {
		if verifyErr := store.verifyProjectIdentity(ctx, cfg.BeadsDir); verifyErr != nil {
			_ = db.Close()
			return nil, verifyErr
		}
	}

	if isLocalHost(cfg.ServerHost) {
		beadsDir := cfg.BeadsDir
		if beadsDir == "" && cfg.Path != "" {
			beadsDir = filepath.Dir(cfg.Path)
		}
		_ = doltserver.EnsurePortFile(beadsDir, cfg.ServerPort)
	}

	// All writers operate on main — transaction isolation via RunInTransaction
	// replaces the former branch-per-worker approach (BD_BRANCH).
	store.branch = "main"

	// GH#2315: Sync CLI remotes into SQL server on store open.
	// After a server restart, dolt_remotes is empty (not persisted across sessions).
	// CLI remotes survive in .dolt/config. Re-register them so DOLT_PUSH/DOLT_PULL work.
	if !cfg.ReadOnly {
		store.syncCLIRemotesToSQL(ctx)
	}

	return store, nil
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
	// go-sql-driver/mysql DSN format: user:password@tcp(host:port)/db?params
	// The password must not contain unescaped special characters that collide
	// with DSN delimiters (@ : / ?). Use go-sql-driver's Config.FormatDSN()
	// which handles escaping correctly.
	dsnCfg := mysql.Config{
		User:                 cfg.ServerUser,
		Passwd:               cfg.ServerPassword,
		Net:                  "tcp",
		Addr:                 fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		DBName:               database,
		ParseTime:            true,
		Timeout:              5 * time.Second,
		ReadTimeout:          10 * time.Second,
		WriteTimeout:         10 * time.Second,
		AllowNativePasswords: true,
	}
	if cfg.ServerTLS {
		dsnCfg.TLSConfig = "true"
	}

	return dsnCfg.FormatDSN()
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

// initSchema creates all tables if they don't exist
func initSchemaOnDB(ctx context.Context, db *sql.DB) error {
	// Fast path: if schema is already at current version, skip initialization.
	// This avoids ~20 DDL statements per bd invocation when schema is current.
	var version int
	err := db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version)
	if err == nil && version >= currentSchemaVersion {
		// Wisps tables are dolt_ignore'd (not persisted in commit history),
		// so they must be recreated on every server session. (GH#2271)
		if err := createIgnoredTables(db); err != nil {
			return err
		}
		// Rebuild status views to match current custom status config.
		// This ensures views stay in sync even after direct SQL config edits.
		if _, err := db.ExecContext(ctx, BuildReadyIssuesView()); err != nil {
			return fmt.Errorf("failed to create ready_issues view: %w", err)
		}
		if _, err := db.ExecContext(ctx, BuildBlockedIssuesView()); err != nil {
			return fmt.Errorf("failed to create blocked_issues view: %w", err)
		}
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
		// GH#2455: Stage only the affected table, not all dirty tables.
		_, _ = db.ExecContext(ctx, "CALL DOLT_ADD('dependencies')")
		_, _ = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'migration: remove fk_dep_depends_on for external references')") // Best effort: migration commit is advisory; schema change already applied
	} else if !strings.Contains(strings.ToLower(err.Error()), "can't drop") &&
		!strings.Contains(strings.ToLower(err.Error()), "doesn't exist") &&
		!strings.Contains(strings.ToLower(err.Error()), "check that it exists") &&
		!strings.Contains(strings.ToLower(err.Error()), "was not found") {
		return fmt.Errorf("failed to drop fk_dep_depends_on: %w", err)
	}

	// Create views — table-backed, no dynamic IN clauses needed.
	if _, err := db.ExecContext(ctx, BuildReadyIssuesView()); err != nil {
		return fmt.Errorf("failed to create ready_issues view: %w", err)
	}
	if _, err := db.ExecContext(ctx, BuildBlockedIssuesView()); err != nil {
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
	_, _ = db.ExecContext(ctx, "CALL DOLT_ADD('config')")
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'schema: update schema_version')"); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			return fmt.Errorf("failed to commit schema_version update: %w", err)
		}
	}

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

// CLIDir returns the directory for dolt CLI operations (push/pull/remote/fetch).
// The actual database lives in a subdirectory of Path() named after the database.
// Use this instead of Path() when running dolt CLI commands that target the
// actual database (e.g., remote add/remove, push, pull).
func (s *DoltStore) CLIDir() string {
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

// Commit creates a Dolt commit with the given message.
//
// GH#2455: Stages all dirty tables EXCEPT config, then commits with '-m'.
// The old '-Am' approach staged ALL dirty tables including config, which
// swept up stale issue_prefix changes from concurrent operations. By
// excluding config from automatic staging, we prevent the corruption.
//
// Callers that intentionally modify config (e.g., CommitPending after
// 'bd config set') must call CommitWithConfig instead.
func (s *DoltStore) Commit(ctx context.Context, message string) (retErr error) {
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

	// GH#2455: Stage all dirty tables EXCEPT config. Query dolt_status for
	// dirty tables and stage each one individually, skipping config to avoid
	// sweeping up stale issue_prefix changes from concurrent operations.
	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		// If dolt_status fails, fall back to nothing (rare edge case).
		return fmt.Errorf("failed to query dolt_status: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan dolt_status: %w", err)
		}
		if table != "config" {
			tables = append(tables, table)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dolt_status: %w", err)
	}

	if len(tables) == 0 {
		return nil // Nothing to commit (all changes were config-only or dolt_ignore'd)
	}

	for _, table := range tables {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
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

// isGitProtocolRemote checks whether the configured remote uses the git wire protocol
// and is available for CLI-based push/pull. Git-protocol remotes (git+ssh://, ssh://,
// git@host:path, git+https://, git://) are routed to CLI operations because the SQL
// server may lack the git credentials, SSH keys, or credential helpers needed for
// network I/O to external git hosts. Returns false when the remote exists only on
// an externally-managed server's filesystem and not in the local dbPath.
func (s *DoltStore) isGitProtocolRemote(ctx context.Context) bool {
	// Check SQL remotes first
	remotes, err := s.ListRemotes(ctx)
	if err == nil {
		for _, r := range remotes {
			if r.Name == s.remote {
				if !doltutil.IsGitProtocolURL(r.URL) {
					return false
				}
				// Verify remote exists in CLI directory before routing to CLI push/pull.
				// When the dolt sql-server is externally managed, remotes may exist only
				// on the server's filesystem, not in the local dbPath.
				return s.CLIDir() != "" && doltutil.FindCLIRemote(s.CLIDir(), s.remote) != ""
			}
		}
	}
	// Fall back to CLI remotes (covers drift where remote exists only in filesystem)
	if s.CLIDir() != "" {
		if url := doltutil.FindCLIRemote(s.CLIDir(), s.remote); url != "" {
			return doltutil.IsGitProtocolURL(url)
		}
	}
	return false
}

// mainRemoteCredentials returns credentials for the main remote, or nil if none.
func (s *DoltStore) mainRemoteCredentials() *remoteCredentials {
	if s.remoteUser == "" && s.remotePassword == "" {
		return nil
	}
	return &remoteCredentials{username: s.remoteUser, password: s.remotePassword}
}

// doltCLIPush shells out to `dolt push` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PUSH times out through the SQL connection.
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
	cmd.Dir = s.CLIDir()
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt push failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIPull shells out to `dolt pull` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PULL times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only.
func (s *DoltStore) doltCLIPull(ctx context.Context, creds *remoteCredentials) error {
	ctx, cancel := context.WithTimeout(ctx, cliExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dolt", "pull", s.remote, s.branch) // #nosec G204 -- fixed command
	cmd.Dir = s.CLIDir()
	creds.applyToCmd(cmd)
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
	ctx, span := doltTracer.Start(ctx, "dolt.push",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", s.remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.mainRemoteCredentials()
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env, avoiding
	// process-wide env var races with concurrent goroutines.
	if s.isGitProtocolRemote(ctx) {
		return s.doltCLIPush(ctx, false, creds)
	}
	// Credential CLI routing: when credentials are set and server is external,
	// route through CLI subprocess so credentials reach the dolt process via
	// cmd.Env (applyToCmd). The SQL path's withEnvCredentials sets process-wide
	// env vars that an external server cannot see.
	if s.shouldUseCLIForCredentials(ctx) {
		return s.doltCLIPush(ctx, false, creds)
	}
	// Cloud auth CLI routing: when cloud storage env vars (AZURE_*, AWS_*,
	// etc.) are set and we're in server mode, route through CLI so the dolt
	// subprocess inherits the current env. The SQL server may not have these
	// vars if it was started in a different context (GH#6).
	if s.shouldUseCLIForCloudAuth() {
		return s.doltCLIPush(ctx, false, creds)
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch); err != nil {
				return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
			}
			return nil
		})
	}
	if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", s.remote, s.branch); err != nil {
		return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// ForcePush force-pushes commits to the remote, overwriting remote changes.
// Use when the remote has uncommitted changes in its working set.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt push --force` to avoid MySQL connection timeouts.
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
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if s.isGitProtocolRemote(ctx) {
		return s.doltCLIPush(ctx, true, creds)
	}
	// Credential CLI routing: when credentials are set and server is external,
	// route through CLI subprocess so credentials reach the dolt process via
	// cmd.Env (applyToCmd). The SQL path's withEnvCredentials sets process-wide
	// env vars that an external server cannot see.
	if s.shouldUseCLIForCredentials(ctx) {
		return s.doltCLIPush(ctx, true, creds)
	}
	// Cloud auth CLI routing (GH#6).
	if s.shouldUseCLIForCloudAuth() {
		return s.doltCLIPush(ctx, true, creds)
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch); err != nil {
				return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
			}
			return nil
		})
	}
	if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH('--force', ?, ?)", s.remote, s.branch); err != nil {
		return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
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
	ctx, span := doltTracer.Start(ctx, "dolt.pull",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", s.remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	// GH#2474: Auto-commit pending changes before pull to prevent
	// "cannot merge with uncommitted changes" errors. Store initialization
	// (schema init, molecule loading, metadata writes) can dirty the working
	// set before the user's pull command runs.
	if !s.readOnly {
		if err := s.Commit(ctx, "auto-commit before pull"); err != nil {
			// "nothing to commit" is fine — working set is already clean
			if !isDoltNothingToCommit(err) {
				return fmt.Errorf("failed to commit pending changes before pull: %w", err)
			}
		}
	}

	creds := s.mainRemoteCredentials()
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if s.isGitProtocolRemote(ctx) {
		if err := s.doltCLIPull(ctx, creds); err != nil {
			return err
		}
		return nil
	}
	// Credential CLI routing: mirrors git-protocol path.
	// Skips pullWithAutoResolve (consistent with git-protocol Pull — CLI manages its
	// own connections and conflict handling).
	if s.shouldUseCLIForCredentials(ctx) {
		if err := s.doltCLIPull(ctx, creds); err != nil {
			return err
		}
		return nil
	}
	// Cloud auth CLI routing (GH#6).
	if s.shouldUseCLIForCloudAuth() {
		return s.doltCLIPull(ctx, creds)
	}
	if s.remoteUser != "" {
		return withEnvCredentials(creds, func() error {
			if err := s.pullWithAutoResolve(ctx, "CALL DOLT_PULL('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch); err != nil {
				return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
			}
			return nil
		})
	}
	if err := s.pullWithAutoResolve(ctx, "CALL DOLT_PULL(?, ?)", s.remote, s.branch); err != nil {
		return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
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
func (s *DoltStore) pullWithAutoResolve(ctx context.Context, query string, args ...any) error {
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

	// Allow commits with conflicts so we can inspect and resolve them.
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to set dolt_allow_commit_conflicts: %w", err)
	}

	_, pullErr := tx.ExecContext(ctx, query, args...)

	// Check for merge conflicts regardless of whether DOLT_PULL errored.
	// Some Dolt versions error on conflicts, others leave them in the working set.
	resolved, resolveErr := s.tryAutoResolveMetadataConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}

	if pullErr != nil && !resolved {
		// Pull failed for a non-conflict reason, or conflicts include non-metadata tables.
		_ = tx.Rollback()
		return pullErr
	}

	return tx.Commit()
}

// tryAutoResolveMetadataConflicts checks if all merge conflicts are on the metadata
// table and resolves them with "theirs" strategy. Returns (true, nil) if all conflicts
// were resolved, (false, nil) if non-metadata conflicts exist, or (false, err) on error.
func (s *DoltStore) tryAutoResolveMetadataConflicts(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		return false, fmt.Errorf("failed to query conflicts: %w", err)
	}

	type conflict struct {
		table string
		count int
	}
	var conflicts []conflict
	for rows.Next() {
		var c conflict
		if err := rows.Scan(&c.table, &c.count); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("failed to scan conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	if len(conflicts) == 0 {
		return false, nil // No conflicts to resolve — error was something else
	}

	// Only auto-resolve if ALL conflicts are on the metadata table.
	for _, c := range conflicts {
		if c.table != "metadata" {
			return false, nil
		}
	}

	// Resolve metadata conflicts with "theirs" — remote values win.
	if _, err := tx.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')"); err != nil {
		return false, fmt.Errorf("failed to resolve metadata conflicts: %w", err)
	}

	// GH#2455: Stage only metadata (the table we resolved), not all dirty tables.
	if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('metadata')"); err != nil {
		return false, fmt.Errorf("failed to stage metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'auto-resolve metadata merge conflicts (GH#2466)')"); err != nil {
		return false, fmt.Errorf("failed to commit resolved conflicts: %w", err)
	}

	return true, nil
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
	return versioncontrolops.CreateBranch(ctx, s.db, name)
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
	if err := versioncontrolops.CheckoutBranch(ctx, s.db, branch); err != nil {
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

	conflicts, err := versioncontrolops.Merge(ctx, s.db, branch, s.commitAuthorString())
	if len(conflicts) > 0 {
		span.SetAttributes(attribute.Int("dolt.conflicts", len(conflicts)))
	}
	return conflicts, err
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

// RebuildStatusViews regenerates the ready_issues and blocked_issues views.
// Views are now table-backed (static SQL), so no custom status parameter needed.
func (s *DoltStore) RebuildStatusViews(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, BuildReadyIssuesView()); err != nil {
		return fmt.Errorf("failed to rebuild ready_issues view: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, BuildBlockedIssuesView()); err != nil {
		return fmt.Errorf("failed to rebuild blocked_issues view: %w", err)
	}
	return nil
}
