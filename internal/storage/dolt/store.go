//go:build cgo

// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// Dolt provides native version control for SQL data with cell-level merge, history queries,
// and federation via Dolt remotes. This backend eliminates the need for JSONL sync layers
// by making the database itself version-controlled.
//
// Dolt capabilities:
//   - Embedded access via github.com/dolthub/driver (no server required)
//   - Native version control (commit, push, pull, branch, merge)
//   - Time-travel queries via AS OF and dolt_history_* tables
//   - Cell-level merge for conflict resolution
//   - Server mode for multi-writer scenarios (federation)
//
// Connection modes:
//   - Embedded: No server required, database/sql interface via dolthub/driver
//   - Server: Connect to running dolt sql-server for multi-writer scenarios
package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	embedded "github.com/dolthub/driver"
	// Import MySQL driver for server mode connections
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db         *sql.DB
	dbPath     string       // Path to Dolt database directory
	closed     atomic.Bool  // Tracks whether Close() has been called
	connStr    string       // Connection string for reconnection
	mu         sync.RWMutex // Protects concurrent access
	readOnly   bool         // True if opened in read-only mode
	serverMode bool         // True if connected to dolt sql-server (vs embedded)
	accessLock *AccessLock  // Advisory flock preventing concurrent dolt LOCK contention

	// embeddedConnector is non-nil only in embedded mode. It must be closed to release
	// filesystem locks held by the embedded engine.
	embeddedConnector *embedded.Connector

	// Watchdog for server mode auto-recovery
	watchdogCancel context.CancelFunc
	watchdogDone   chan struct{}

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
	OpenTimeout    time.Duration // Advisory lock timeout (0 = no advisory lock)

	// Server mode options (federation)
	ServerMode     bool   // Connect to dolt sql-server instead of embedded
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

const embeddedOpenMaxElapsed = 30 * time.Second

func newEmbeddedOpenBackoff() backoff.BackOff {
	// BackOff implementations are stateful; always return a fresh instance.
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = embeddedOpenMaxElapsed
	return bo
}

// Server mode retry configuration.
// Server mode uses go-sql-driver/mysql which doesn't have built-in retry like the
// embedded driver. We add retry for transient connection errors (stale pool connections,
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
	return false
}

// withRetry executes an operation with retry for transient errors.
// Only active in server mode; embedded mode has driver-level retry.
func (s *DoltStore) withRetry(ctx context.Context, op func() error) error {
	if !s.serverMode {
		return op()
	}

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

// execContext wraps s.db.ExecContext with server-mode retry for transient errors.
func (s *DoltStore) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.withRetry(ctx, func() error {
		var execErr error
		result, execErr = s.db.ExecContext(ctx, query, args...)
		return execErr
	})
	return result, err
}

// queryContext wraps s.db.QueryContext with server-mode retry for transient errors.
func (s *DoltStore) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := s.withRetry(ctx, func() error {
		var queryErr error
		rows, queryErr = s.db.QueryContext(ctx, query, args...)
		return queryErr
	})
	return rows, err
}

// queryRowContext wraps s.db.QueryRowContext with server-mode retry for transient errors.
// The scan function receives the *sql.Row and should call .Scan() on it.
func (s *DoltStore) queryRowContext(ctx context.Context, scan func(*sql.Row) error, query string, args ...any) error {
	return s.withRetry(ctx, func() error {
		row := s.db.QueryRowContext(ctx, query, args...)
		return scan(row)
	})
}

// New creates a new Dolt storage backend
func New(ctx context.Context, cfg *Config) (*DoltStore, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("database path is required")
	}

	// Default values
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

	// Server mode defaults
	if cfg.ServerMode {
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
	}

	// Embedded-only: create local directory and acquire access lock.
	// In server mode, the database lives on the remote dolt sql-server;
	// creating a local dolt/ directory would shadow the server connection
	// with an empty embedded db (see bd-vyr).
	// Remote credentials for Hosted Dolt push/pull (env vars take precedence)
	if cfg.RemoteUser == "" {
		cfg.RemoteUser = os.Getenv("DOLT_REMOTE_USER")
	}
	if cfg.RemotePassword == "" {
		cfg.RemotePassword = os.Getenv("DOLT_REMOTE_PASSWORD")
	}

	var absPath string
	var accessLock *AccessLock
	if !cfg.ServerMode {
		if err := os.MkdirAll(cfg.Path, 0o750); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}

		// IMPORTANT: Use an absolute path for embedded DSNs.
		//
		// The embedded driver sets its internal filesystem working directory to Config.Directory
		// and also passes the directory path through to lower layers. If we pass a relative path,
		// the working-directory stacking can effectively double it (e.g. ".beads/dolt/.beads/dolt").
		var err error
		absPath, err = filepath.Abs(cfg.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path: %w", err)
		}

		// Acquire advisory flock before opening dolt (embedded mode only).
		// This prevents multiple bd processes from competing for dolt's internal LOCK file.
		// Set BD_SKIP_ACCESS_LOCK=1 to bypass flock for testing whether Dolt's internal
		// locking is sufficient. See bd-39gso for testing plan.
		if cfg.OpenTimeout > 0 && os.Getenv("BD_SKIP_ACCESS_LOCK") == "" {
			exclusive := !cfg.ReadOnly
			var lockErr error
			accessLock, lockErr = AcquireAccessLock(absPath, exclusive, cfg.OpenTimeout)
			if lockErr != nil {
				return nil, fmt.Errorf("failed to acquire dolt access lock: %w", lockErr)
			}
		}
	}

	var db *sql.DB
	var connStr string
	var embeddedConnector *embedded.Connector
	var err error

	if cfg.ServerMode {
		// Fail-fast TCP check before MySQL protocol initialization.
		// This gives an immediate, clear error if the Dolt server isn't running,
		// rather than waiting for MySQL driver timeouts.
		addr := net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			if accessLock != nil {
				accessLock.Release()
			}
			return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\nThe Dolt server may not be running. Try:\n  gt dolt start    # If using Gas Town\n  dolt sql-server  # Manual start in database directory",
				addr, err)
		}
		_ = conn.Close()

		// Server mode: connect via MySQL protocol to dolt sql-server
		db, connStr, err = openServerConnection(ctx, cfg)
	} else {
		// Embedded mode:
		// - Perform initialization as explicit units of work (each with its own connector).
		// - Then open a fresh connector/DB for the returned store instance.
		initDSN := fmt.Sprintf(
			"file://%s?commitname=%s&commitemail=%s",
			absPath, cfg.CommitterName, cfg.CommitterEmail,
		)
		dbDSN := fmt.Sprintf(
			"file://%s?commitname=%s&commitemail=%s&database=%s",
			absPath, cfg.CommitterName, cfg.CommitterEmail, cfg.Database,
		)

		configureRetries := func(c *embedded.Config) {
			// Enable driver open retries for embedded usage.
			c.BackOff = newEmbeddedOpenBackoff()
		}

		// UOW 1: ensure database exists. Skip in read-only mode.
		if !cfg.ReadOnly {
			if err := withEmbeddedDolt(ctx, initDSN, configureRetries, func(ctx context.Context, db *sql.DB) error {
				_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database))
				return err
			}); err != nil {
				return nil, fmt.Errorf("failed to create dolt database: %w", err)
			}
		}

		// UOW 2: initialize schema (idempotent). Skip in read-only mode.
		if !cfg.ReadOnly {
			if err := withEmbeddedDolt(ctx, dbDSN, configureRetries, func(ctx context.Context, db *sql.DB) error {
				return initSchemaOnDB(ctx, db)
			}); err != nil {
				return nil, fmt.Errorf("failed to initialize schema: %w", err)
			}
		}

		// Open the store connection (fresh connector for subsequent work).
		db, connStr, embeddedConnector, err = openEmbeddedConnection(dbDSN)
	}

	if err != nil {
		if accessLock != nil {
			accessLock.Release()
		}
		return nil, err
	}

	// Test connection
	// IMPORTANT: In embedded mode, do not use a caller-supplied ctx to open the first
	// underlying connection. Many tests (and some call sites) pass contexts that are
	// canceled shortly after New() returns; the embedded driver derives a session context
	// from Connect(ctx) and reuses it across statements. We force the initial connection
	// to be created with a non-canceling context to avoid poisoning the connection pool.
	pingCtx := ctx
	if embeddedConnector != nil {
		pingCtx = context.Background()
	}
	if pingCtx == nil {
		pingCtx = context.Background()
	}
	if err := db.PingContext(pingCtx); err != nil {
		// Ensure we don't leak filesystem locks if embedded open fails after creating a connector.
		_ = db.Close()
		if embeddedConnector != nil {
			_ = embeddedConnector.Close()
		}
		if accessLock != nil {
			accessLock.Release()
		}
		return nil, fmt.Errorf("failed to ping Dolt database: %w", err)
	}

	store := &DoltStore{
		db:                db,
		dbPath:            absPath,
		connStr:           connStr,
		embeddedConnector: embeddedConnector,
		committerName:     cfg.CommitterName,
		committerEmail:    cfg.CommitterEmail,
		remote:            cfg.Remote,
		branch:            "main",
		remoteUser:        cfg.RemoteUser,
		remotePassword:    cfg.RemotePassword,
		readOnly:          cfg.ReadOnly,
		serverMode:        cfg.ServerMode,
		accessLock:        accessLock,
	}

	// Schema initialization:
	// - Embedded mode: already performed above as an explicit unit of work.
	// - Server mode: still needs to initialize schema here (idempotent).
	if cfg.ServerMode && !cfg.ReadOnly {
		if err := store.initSchema(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	// Branch-per-polecat: if BD_BRANCH is set, checkout polecat-specific branch.
	// Each polecat writes to its own Dolt branch to eliminate optimistic lock
	// contention between concurrent writers. Merges happen at gt done time.
	// Only applies in server mode (embedded mode doesn't support concurrent writers).
	if bdBranch := os.Getenv("BD_BRANCH"); bdBranch != "" && cfg.ServerMode {
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
	}

	// Start watchdog for server mode auto-recovery
	store.startWatchdog(cfg)

	return store, nil
}

// openEmbeddedConnection opens a connection using the embedded Dolt driver
func openEmbeddedConnection(dsn string) (*sql.DB, string, *embedded.Connector, error) {
	openCfg, err := embedded.ParseDSN(dsn)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to parse Dolt DSN: %w", err)
	}
	openCfg.BackOff = newEmbeddedOpenBackoff()

	connector, err := embedded.NewConnector(openCfg)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to create Dolt connector: %w", err)
	}
	db := sql.OpenDB(connector)

	// Configure connection pool
	// Dolt embedded mode is single-writer like SQLite
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// NOTE: connector must be closed by the caller to release filesystem locks.
	// DoltStore.Close() will handle this.
	return db, dsn, connector, nil
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
				return nil, "", fmt.Errorf("failed to connect to Dolt server at %s:%d: %w\n\nThe Dolt server may not be running. Try:\n  gt dolt start    # If using Gas Town\n  dolt sql-server  # Manual start in database directory",
					cfg.ServerHost, cfg.ServerPort, err)
			}
			return nil, "", fmt.Errorf("failed to create database: %w", err)
		}
		// Database already exists - that's fine, continue
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
	// See SQLite migration 025_remove_depends_on_fk.go for design context.
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
	// For embedded mode, ensure the underlying engine is closed to release filesystem locks.
	if s.embeddedConnector != nil {
		cerr := doltutil.CloseWithTimeout("embeddedConnector", s.embeddedConnector.Close)
		// Ignore context cancellation noise from Dolt shutdown plumbing.
		if cerr != nil && !errors.Is(cerr, context.Canceled) {
			err = errors.Join(err, cerr)
		}
		s.embeddedConnector = nil
	}
	s.db = nil
	// Release advisory lock after db and connector are closed
	if s.accessLock != nil {
		s.accessLock.Release()
		s.accessLock = nil
	}
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
