// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// Dolt provides native version control for SQL data with cell-level merge, history queries,
// and federation via Dolt remotes. This backend eliminates the need for JSONL sync layers
// by making the database itself version-controlled.
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
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	// Import MySQL driver for server mode connections
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db       *sql.DB
	dbPath   string       // Path to Dolt database directory
	closed   atomic.Bool  // Tracks whether Close() has been called
	connStr  string       // Connection string for reconnection
	mu       sync.RWMutex // Protects concurrent access
	readOnly bool         // True if opened in read-only mode

	// Watchdog for server mode auto-recovery
	watchdogCancel context.CancelFunc
	watchdogDone   chan struct{}

	// Per-invocation caches (lifetime = DoltStore lifetime)
	customStatusCache  []string // cached result of GetCustomStatuses
	customStatusCached bool     // true once customStatusCache has been populated
	customTypeCache    []string // cached result of GetCustomTypes
	customTypeCached   bool     // true once customTypeCache has been populated
	blockedIDsCache    []string // cached result of computeBlockedIDs
	blockedIDsCacheMap map[string]bool
	blockedIDsCached   bool // true once blockedIDsCache has been populated
	cacheMu            sync.Mutex

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
	Path           string        // Path to Dolt database directory
	CommitterName  string        // Git-style committer name
	CommitterEmail string        // Git-style committer email
	Remote         string        // Default remote name (e.g., "origin")
	Database       string        // Database name within Dolt (default: "beads")
	ReadOnly       bool          // Open in read-only mode (skip schema init)

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

	// Watchdog options
	DisableWatchdog bool // Disable server health monitoring (default: enabled in server mode)
}

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
func (s *DoltStore) withRetry(ctx context.Context, op func() error) error {
	bo := newServerRetryBackoff()
	return backoff.Retry(func() error {
		err := op()
		if err != nil && isRetryableError(err) {
			return err // Retryable - backoff will retry
		}
		if err != nil {
			return backoff.Permanent(err) // Non-retryable - stop immediately
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

// execContext wraps a write statement in an explicit BEGIN/COMMIT to ensure
// durability when the Dolt server runs with autocommit disabled (the default
// when started with --no-auto-commit). Without this, writes remain in an
// uncommitted implicit transaction that Dolt rolls back on connection close,
// causing silent data loss for callers that do not use db.BeginTx themselves.
func (s *DoltStore) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
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
	return result, wrapLockError(err)
}

// queryContext wraps s.db.QueryContext with retry for transient errors.
func (s *DoltStore) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := s.withRetry(ctx, func() error {
		var queryErr error
		rows, queryErr = s.db.QueryContext(ctx, query, args...)
		return queryErr
	})
	return rows, wrapLockError(err)
}

// queryRowContext wraps s.db.QueryRowContext with retry for transient errors.
// The scan function receives the *sql.Row and should call .Scan() on it.
func (s *DoltStore) queryRowContext(ctx context.Context, scan func(*sql.Row) error, query string, args ...any) error {
	return wrapLockError(s.withRetry(ctx, func() error {
		row := s.db.QueryRowContext(ctx, query, args...)
		return scan(row)
	}))
}

// applyConfigDefaults fills in default values for unset Config fields.
func applyConfigDefaults(cfg *Config) {
	if cfg.Database == "" {
		cfg.Database = "beads"
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
		cfg.ServerHost = "127.0.0.1"
	}
	if cfg.ServerPort == 0 {
		cfg.ServerPort = DefaultSQLPort
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

	return newServerMode(ctx, cfg)
}

// newServerMode creates a DoltStore connected to a running dolt sql-server.
// This path is pure Go and does not require CGO.
func newServerMode(ctx context.Context, cfg *Config) (*DoltStore, error) {
	// Fail-fast TCP check before MySQL protocol initialization.
	// This gives an immediate, clear error if the Dolt server isn't running,
	// rather than waiting for MySQL driver timeouts.
	addr := net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
	conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dialErr != nil {
		return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\nThe Dolt server may not be running. Try:\n  gt dolt start    # If using Gas Town\n  bd dolt start    # If using Beads directly",
			addr, dialErr)
	}
	_ = conn.Close()

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
		connStr:        connStr,
		committerName:  cfg.CommitterName,
		committerEmail: cfg.CommitterEmail,
		remote:         cfg.Remote,
		branch:         "main",
		remoteUser:     cfg.RemoteUser,
		remotePassword: cfg.RemotePassword,
		readOnly:       cfg.ReadOnly,
	}

	// Schema initialization on main (tracked tables only).
	// This ensures main has committed schema before we create branches from it.
	// Must happen before branch checkout so new branches inherit the schema.
	if !cfg.ReadOnly {
		if err := store.initSchema(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	// Branch-per-polecat: if BD_BRANCH is set, checkout polecat-specific branch.
	// Each polecat writes to its own Dolt branch to eliminate optimistic lock
	// contention between concurrent writers. Merges happen at gt done time.
	if bdBranch := os.Getenv("BD_BRANCH"); bdBranch != "" {
		// Force single connection to ensure branch checkout applies to all operations.
		// This is safe because each polecat is a separate bd process.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", bdBranch); err != nil {
			// Branch doesn't exist — auto-create from current branch, then checkout.
			// This makes polecats self-healing: they create their own branches
			// if Gas Town hasn't pre-created them (race condition, cleanup, etc.).
			if _, createErr := db.ExecContext(ctx, "CALL DOLT_BRANCH(?)", bdBranch); createErr != nil {
				_ = store.Close()
				return nil, fmt.Errorf("failed to create Dolt branch %s: %w (checkout error: %v)", bdBranch, createErr, err)
			}
			if _, coErr := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", bdBranch); coErr != nil {
				_ = store.Close()
				return nil, fmt.Errorf("failed to checkout Dolt branch %s after creation: %w", bdBranch, coErr)
			}
		}
		store.branch = bdBranch

		// Re-run schema init on the working branch. Untracked tables
		// (wisps, wisp_*) exist only in the working set and are lost on
		// branch checkout. Re-init is idempotent and recreates them.
		if !cfg.ReadOnly {
			if err := store.initSchema(ctx); err != nil {
				_ = store.Close()
				return nil, fmt.Errorf("failed to initialize schema on branch %s: %w", bdBranch, err)
			}
		}
	}

	// Start watchdog for server mode auto-recovery
	store.startWatchdog(cfg)

	return store, nil
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

	params := "parseTime=true"
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
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
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
	if err := validateDatabaseName(cfg.Database); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("invalid database name %q: %w", cfg.Database, err)
	}
	_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database)) //nolint:gosec // G201: cfg.Database validated by validateDatabaseName above
	if err != nil {
		// Dolt may return error 1007 even with IF NOT EXISTS - ignore if database already exists
		errLower := strings.ToLower(err.Error())
		if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
			_ = db.Close()
			// Check for connection refused - server likely not running
			if strings.Contains(errLower, "connection refused") || strings.Contains(errLower, "connect: connection refused") {
				return nil, "", fmt.Errorf("failed to connect to Dolt server at %s:%d: %w\n\nThe Dolt server may not be running. Try:\n  gt dolt start    # If using Gas Town\n  bd dolt start # If using Beads directly",
					cfg.ServerHost, cfg.ServerPort, err)
			}
			return nil, "", fmt.Errorf("failed to create database: %w", err)
		}
		// Database already exists - that's fine, continue
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

// Close closes the database connection
func (s *DoltStore) Close() error {
	s.closed.Store(true)
	// Stop watchdog before taking the lock (watchdog may hold RLock)
	s.stopWatchdog()
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
	return err
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
func (s *DoltStore) Commit(ctx context.Context, message string) error {
	// NOTE: In SQL procedure mode, Dolt defaults author to the authenticated SQL user
	// (e.g. root@localhost). Always pass an explicit author for deterministic history.
	_, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", message, s.commitAuthorString())
	if err != nil {
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
	// Check if there are any uncommitted changes
	status, err := s.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	if len(status.Staged) == 0 && len(status.Unstaged) == 0 {
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

	if len(parts) == 0 {
		return fmt.Sprintf("bd: batch commit by %s", actor)
	}
	return fmt.Sprintf("bd: batch commit by %s — %s", actor, strings.Join(parts, ", "))
}

// Push pushes commits to the remote.
// When remote credentials are configured (for Hosted Dolt), sets DOLT_REMOTE_PASSWORD
// env var and passes --user flag to authenticate.
func (s *DoltStore) Push(ctx context.Context) error {
	if s.remoteUser != "" {
		federationEnvMutex.Lock()
		cleanup := setFederationCredentials(s.remoteUser, s.remotePassword)
		defer func() {
			cleanup()
			federationEnvMutex.Unlock()
		}()
		_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
		if err != nil {
			return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH(?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// ForcePush force-pushes commits to the remote, overwriting remote changes.
// Use when the remote has uncommitted changes in its working set.
func (s *DoltStore) ForcePush(ctx context.Context) error {
	if s.remoteUser != "" {
		federationEnvMutex.Lock()
		cleanup := setFederationCredentials(s.remoteUser, s.remotePassword)
		defer func() {
			cleanup()
			federationEnvMutex.Unlock()
		}()
		_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
		if err != nil {
			return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH('--force', ?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to force push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// Pull pulls changes from the remote.
// Passes branch explicitly to avoid "did not specify a branch" errors.
// When remote credentials are configured (for Hosted Dolt), sets DOLT_REMOTE_PASSWORD
// env var and passes --user flag to authenticate.
func (s *DoltStore) Pull(ctx context.Context) error {
	if s.remoteUser != "" {
		federationEnvMutex.Lock()
		cleanup := setFederationCredentials(s.remoteUser, s.remotePassword)
		defer func() {
			cleanup()
			federationEnvMutex.Unlock()
		}()
		_, err := s.db.ExecContext(ctx, "CALL DOLT_PULL('--user', ?, ?, ?)", s.remoteUser, s.remote, s.branch)
		if err != nil {
			return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PULL(?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to pull from %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// Branch creates a new branch
func (s *DoltStore) Branch(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_BRANCH(?)", name)
	if err != nil {
		return fmt.Errorf("failed to create branch %s: %w", name, err)
	}
	return nil
}

// Checkout switches to the specified branch
func (s *DoltStore) Checkout(ctx context.Context, branch string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch)
	if err != nil {
		return fmt.Errorf("failed to checkout branch %s: %w", branch, err)
	}
	s.branch = branch
	return nil
}

// Merge merges the specified branch into the current branch.
// Returns any merge conflicts if present. Implements storage.VersionedStorage.
func (s *DoltStore) Merge(ctx context.Context, branch string) ([]storage.Conflict, error) {
	// DOLT_MERGE may create a merge commit; pass explicit author for determinism.
	_, err := s.db.ExecContext(ctx, "CALL DOLT_MERGE('--author', ?, ?)", s.commitAuthorString(), branch)
	if err != nil {
		// Check if the error is due to conflicts
		conflicts, conflictErr := s.GetConflicts(ctx)
		if conflictErr == nil && len(conflicts) > 0 {
			return conflicts, nil
		}
		return nil, fmt.Errorf("failed to merge branch %s: %w", branch, err)
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
