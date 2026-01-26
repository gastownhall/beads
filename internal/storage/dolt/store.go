// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// Dolt provides native version control for SQL data with cell-level merge, history queries,
// and federation via Dolt remotes. This backend eliminates the need for JSONL sync layers
// by making the database itself version-controlled.
//
// Key differences from SQLite backend:
//   - Uses github.com/dolthub/driver for embedded Dolt access
//   - Supports version control operations (commit, push, pull, branch, merge)
//   - History queries via AS OF and dolt_history_* tables
//   - Cell-level merge instead of line-level JSONL merge
//
// Connection modes:
//   - Embedded: No server required, database/sql interface via dolthub/driver
//   - Server: Connect to running dolt sql-server for multi-writer scenarios
package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// Import Dolt embedded driver
	_ "github.com/dolthub/driver"
	// Import MySQL driver for server mode connections
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db       *sql.DB
	dbPath   string       // Path to Dolt database directory
	closed   atomic.Bool  // Tracks whether Close() has been called
	connStr  string       // Connection string for reconnection
	mu       sync.RWMutex // Protects concurrent access
	readOnly bool         // True if opened in read-only mode

	// Version control config
	committerName  string
	committerEmail string
	remote         string // Default remote for push/pull
	branch         string // Current branch
}

// Config holds Dolt database configuration
type Config struct {
	Path           string        // Path to Dolt database directory
	CommitterName  string        // Git-style committer name
	CommitterEmail string        // Git-style committer email
	Remote         string        // Default remote name (e.g., "origin")
	Database       string        // Database name within Dolt (default: "beads")
	ReadOnly       bool          // Open in read-only mode (skip schema init)
	LockRetries    int           // Number of retries on lock contention (default: 30)
	LockRetryDelay time.Duration // Initial retry delay (default: 100ms, doubles each retry)

	// Server mode options (federation)
	ServerMode     bool   // Connect to dolt sql-server instead of embedded
	ServerHost     string // Server host (default: 127.0.0.1)
	ServerPort     int    // Server port (default: 3306)
	ServerUser     string // MySQL user (default: root)
	ServerPassword string // MySQL password (default: empty, can be set via BEADS_DOLT_PASSWORD)
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
	// Lock retry defaults
	if cfg.LockRetries == 0 {
		cfg.LockRetries = 30 // ~6 seconds with exponential backoff
	}
	if cfg.LockRetryDelay == 0 {
		cfg.LockRetryDelay = 100 * time.Millisecond
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

	// Ensure directory exists
	if err := os.MkdirAll(cfg.Path, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Clean up stale LOCK file if present (for embedded mode only)
	// The Dolt embedded driver creates a LOCK file in .dolt/noms/ that may persist
	// after crashes or unexpected termination. This causes "database is read only" errors.
	if !cfg.ServerMode {
		if err := cleanupStaleDoltLock(cfg.Path, cfg.Database); err != nil {
			// Log but don't fail - the lock may be legitimately held
			fmt.Fprintf(os.Stderr, "Warning: could not check/clean Dolt lock: %v\n", err)
		}
	}

	var db *sql.DB
	var connStr string
	var err error

	if cfg.ServerMode {
		// Server mode: connect via MySQL protocol to dolt sql-server
		db, connStr, err = openServerConnection(ctx, cfg)
	} else {
		// Embedded mode: use Dolt driver directly
		db, connStr, err = openEmbeddedConnection(ctx, cfg)
	}

	if err != nil {
		return nil, err
	}

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping Dolt database: %w", err)
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	store := &DoltStore{
		db:             db,
		dbPath:         absPath,
		connStr:        connStr,
		committerName:  cfg.CommitterName,
		committerEmail: cfg.CommitterEmail,
		remote:         cfg.Remote,
		branch:         "main",
		readOnly:       cfg.ReadOnly,
	}

	// Initialize schema (skip for read-only mode)
	if !cfg.ReadOnly {
		if err := store.initSchema(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	return store, nil
}

// openEmbeddedConnection opens a connection using the embedded Dolt driver
// with retry logic for transient errors (lock contention, format version issues)
func openEmbeddedConnection(ctx context.Context, cfg *Config) (*sql.DB, string, error) {
	// Build connection string - we use a single connection and switch databases using USE.
	// The Dolt embedded driver shares internal state between connections to the same path.
	connStr := fmt.Sprintf(
		"file://%s?commitname=%s&commitemail=%s",
		cfg.Path, cfg.CommitterName, cfg.CommitterEmail)

	// Retry logic for transient Dolt errors (lock contention, format version issues)
	var db *sql.DB
	var lastErr error
	retryDelay := cfg.LockRetryDelay

	for attempt := 0; attempt <= cfg.LockRetries; attempt++ {
		if attempt > 0 {
			// Log transient error for debugging
			fmt.Fprintf(os.Stderr, "Dolt transient error detected (attempt %d/%d), retrying in %v...\n",
				attempt, cfg.LockRetries, retryDelay)
			time.Sleep(retryDelay)
			// Exponential backoff
			retryDelay *= 2
		}

		db, lastErr = sql.Open("dolt", connStr)
		if lastErr != nil {
			if isTransientDoltError(lastErr) {
				continue // Retry
			}
			return nil, "", fmt.Errorf("failed to open Dolt database: %w", lastErr)
		}

		// Create the database if it doesn't exist
		_, lastErr = db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", cfg.Database))
		if lastErr != nil {
			if isTransientDoltError(lastErr) {
				_ = db.Close()
				continue // Retry
			}
			_ = db.Close()
			return nil, "", fmt.Errorf("failed to create database: %w", lastErr)
		}

		// Switch to the target database using USE
		_, lastErr = db.ExecContext(ctx, fmt.Sprintf("USE %s", cfg.Database))
		if lastErr != nil {
			if isTransientDoltError(lastErr) {
				_ = db.Close()
				continue // Retry
			}
			_ = db.Close()
			return nil, "", fmt.Errorf("failed to switch to database %s: %w", cfg.Database, lastErr)
		}

		// Configure connection pool
		// Dolt embedded mode is single-writer like SQLite
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)

		// Test connection
		lastErr = db.PingContext(ctx)
		if lastErr != nil {
			if isTransientDoltError(lastErr) {
				_ = db.Close()
				continue // Retry
			}
			_ = db.Close()
			return nil, "", fmt.Errorf("failed to ping Dolt database: %w", lastErr)
		}

		// Success! Break out of retry loop
		break
	}

	// Check if all retries exhausted
	if lastErr != nil {
		return nil, "", fmt.Errorf("failed to connect to Dolt database after %d retries: %w", cfg.LockRetries, lastErr)
	}

	return db, connStr, nil
}

// openServerConnection opens a connection to a dolt sql-server via MySQL protocol
func openServerConnection(ctx context.Context, cfg *Config) (*sql.DB, string, error) {
	// DSN format: user:password@tcp(host:port)/database?parseTime=true
	// parseTime=true tells the MySQL driver to parse DATETIME/TIMESTAMP to time.Time
	var connStr string
	if cfg.ServerPassword != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
			cfg.ServerUser, cfg.ServerPassword, cfg.ServerHost, cfg.ServerPort, cfg.Database)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true",
			cfg.ServerUser, cfg.ServerHost, cfg.ServerPort, cfg.Database)
	}

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
	var initConnStr string
	if cfg.ServerPassword != "" {
		initConnStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=true",
			cfg.ServerUser, cfg.ServerPassword, cfg.ServerHost, cfg.ServerPort)
	} else {
		initConnStr = fmt.Sprintf("%s@tcp(%s:%d)/?parseTime=true",
			cfg.ServerUser, cfg.ServerHost, cfg.ServerPort)
	}
	initDB, err := sql.Open("mysql", initConnStr)
	if err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("failed to open init connection: %w", err)
	}
	defer func() { _ = initDB.Close() }()

	_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", cfg.Database))
	if err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("failed to create database: %w", err)
	}

	return db, connStr, nil
}

// initSchema creates all tables if they don't exist
func (s *DoltStore) initSchema(ctx context.Context) error {
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
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
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
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to insert default config: %w", err)
		}
	}

	// Create views
	if _, err := s.db.ExecContext(ctx, readyIssuesView); err != nil {
		return fmt.Errorf("failed to create ready_issues view: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, blockedIssuesView); err != nil {
		return fmt.Errorf("failed to create blocked_issues view: %w", err)
	}

	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Path returns the database directory path
func (s *DoltStore) Path() string {
	return s.dbPath
}

// IsClosed returns true if Close() has been called
func (s *DoltStore) IsClosed() bool {
	return s.closed.Load()
}

// UnderlyingDB returns the underlying *sql.DB connection
func (s *DoltStore) UnderlyingDB() *sql.DB {
	return s.db
}

// UnderlyingConn returns a connection from the pool
func (s *DoltStore) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return s.db.Conn(ctx)
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

// Push pushes commits to the remote
func (s *DoltStore) Push(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PUSH(?, ?)", s.remote, s.branch)
	if err != nil {
		return fmt.Errorf("failed to push to %s/%s: %w", s.remote, s.branch, err)
	}
	return nil
}

// Pull pulls changes from the remote
func (s *DoltStore) Pull(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_PULL(?)", s.remote)
	if err != nil {
		return fmt.Errorf("failed to pull from %s: %w", s.remote, err)
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

// MergeAllowUnrelated merges the specified branch allowing unrelated histories.
// This is needed for initial federation sync between independently initialized towns.
// Returns any merge conflicts if present.
func (s *DoltStore) MergeAllowUnrelated(ctx context.Context, branch string) ([]storage.Conflict, error) {
	// DOLT_MERGE may create a merge commit; pass explicit author for determinism.
	_, err := s.db.ExecContext(ctx, "CALL DOLT_MERGE('--allow-unrelated-histories', '--author', ?, ?)", s.commitAuthorString(), branch)
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

// isTransientDoltError detects if an error is transient and should be retried.
// This includes lock errors and format version errors which can occur during
// concurrent access when the manifest is being updated.
func isTransientDoltError(err error) bool {
	if err == nil {
		return false
	}
	// Check lock errors first
	if isLockError(err) {
		return true
	}
	// Check for format version errors - these can occur transiently during
	// concurrent manifest updates (e.g., during push/pull operations)
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "invalid format version") ||
		strings.Contains(errStr, "failed to load database") ||
		strings.Contains(errStr, "manifest") && strings.Contains(errStr, "invalid")
}

// isLockError checks if an error is related to lock contention
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "lock") ||
		strings.Contains(errStr, "database is read only") ||
		strings.Contains(errStr, "resource temporarily unavailable")
}

// cleanupStaleDoltLock removes stale LOCK files from the Dolt noms directory.
// The embedded Dolt driver creates a LOCK file that persists after crashes,
// causing subsequent opens to fail with "database is read only" errors.
func cleanupStaleDoltLock(dbPath string, database string) error {
	// The LOCK file is in the noms directory under .dolt
	// For a database at /path/to/dolt with database name "beads",
	// the lock is at /path/to/dolt/beads/.dolt/noms/LOCK
	lockPath := filepath.Join(dbPath, database, ".dolt", "noms", "LOCK")

	info, err := os.Stat(lockPath)
	if os.IsNotExist(err) {
		// No lock file, nothing to do
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat lock file: %w", err)
	}

	// Check if lock file is empty (Dolt creates empty LOCK files)
	// An empty LOCK file is likely stale - the driver should have released it
	if info.Size() == 0 {
		// Check how old the lock is - if it's been more than a few seconds,
		// it's likely stale from a crashed process
		age := time.Since(info.ModTime())
		if age > 5*time.Second {
			fmt.Fprintf(os.Stderr, "Removing stale Dolt LOCK file (age: %v)\n", age.Round(time.Second))
			if err := os.Remove(lockPath); err != nil {
				return fmt.Errorf("remove stale lock: %w", err)
			}
			return nil
		}
		// Lock is recent, might be held by another process
		return nil
	}

	// Non-empty lock file - might contain PID info, don't touch it
	return nil
}
