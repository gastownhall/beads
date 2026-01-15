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

	// Import Dolt driver
	_ "github.com/dolthub/driver"
)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db          *sql.DB
	dbPath      string       // Path to Dolt database directory
	closed      atomic.Bool  // Tracks whether Close() has been called
	connStr     string       // Connection string for reconnection
	mu          sync.RWMutex // Protects concurrent access
	readOnly    bool         // True if opened in read-only mode

	// Version control config
	committerName  string
	committerEmail string
	remote         string // Default remote for push/pull
	branch         string // Current branch
}

// Config holds Dolt database configuration
type Config struct {
	Path           string // Path to Dolt database directory
	CommitterName  string // Git-style committer name
	CommitterEmail string // Git-style committer email
	Remote         string // Default remote name (e.g., "origin")
	Database       string // Database name within Dolt (default: "beads")
	ReadOnly       bool   // Open in read-only mode (skip schema init)
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

	// Ensure directory exists
	if err := os.MkdirAll(cfg.Path, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// First, connect without specifying a database to create it if needed
	initConnStr := fmt.Sprintf(
		"file://%s?commitname=%s&commitemail=%s",
		cfg.Path, cfg.CommitterName, cfg.CommitterEmail)

	initDB, err := sql.Open("dolt", initConnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open Dolt for initialization: %w", err)
	}

	// Create the database if it doesn't exist
	_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", cfg.Database))
	if err != nil {
		_ = initDB.Close() // nolint:gosec // G104: error ignored on early return
		return nil, fmt.Errorf("failed to create database: %w", err)
	}
	_ = initDB.Close() // nolint:gosec // G104: connection no longer needed

	// Now connect with the database specified
	connStr := fmt.Sprintf(
		"file://%s?commitname=%s&commitemail=%s&database=%s",
		cfg.Path, cfg.CommitterName, cfg.CommitterEmail, cfg.Database)

	db, err := sql.Open("dolt", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open Dolt database: %w", err)
	}

	// Configure connection pool
	// Dolt embedded mode is single-writer like SQLite
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

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

// Commit creates a Dolt commit with the given message
func (s *DoltStore) Commit(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", message)
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

// Merge merges the specified branch into the current branch
func (s *DoltStore) Merge(ctx context.Context, branch string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_MERGE(?)", branch)
	if err != nil {
		return fmt.Errorf("failed to merge branch %s: %w", branch, err)
	}
	return nil
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
		var statusInt int
		if err := rows.Scan(&tableName, &staged, &statusInt); err != nil {
			return nil, fmt.Errorf("failed to scan status: %w", err)
		}
		entry := StatusEntry{Table: tableName, Status: statusInt}
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
	Status int // 1=new, 2=modified, 3=deleted
}
