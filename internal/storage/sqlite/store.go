// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	// Import SQLite driver
	sqlite3 "github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/tetratelabs/wazero"
)

// SQLiteStorage implements the Storage interface using SQLite
type SQLiteStorage struct {
	db     *sql.DB
	dbPath string
	closed atomic.Bool // Tracks whether Close() has been called
}

// setupWASMCache configures WASM compilation caching to reduce SQLite startup time.
// Returns the cache directory path (empty string if using in-memory cache).
//
// Cache behavior:
//   - Location: ~/.cache/beads/wasm/ (platform-specific via os.UserCacheDir)
//   - Version management: wazero automatically keys cache by its version
//   - Cleanup: Old versions remain harmless (~5-10MB each); manual cleanup if needed
//   - Fallback: Uses in-memory cache if filesystem cache creation fails
//
// Performance impact:
//   - First run: ~220ms (compile + cache)
//   - Subsequent runs: ~20ms (load from cache)
func setupWASMCache() string {
	cacheDir := ""
	if userCache, err := os.UserCacheDir(); err == nil {
		cacheDir = filepath.Join(userCache, "beads", "wasm")
	}

	var cache wazero.CompilationCache
	if cacheDir != "" {
		// Try file-system cache first (persistent across runs)
		if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cache = c
			// Optional: log cache location for debugging
			// fmt.Fprintf(os.Stderr, "WASM cache: %s\n", cacheDir)
		}
	}

	// Fallback to in-memory cache if dir creation failed
	if cache == nil {
		cache = wazero.NewCompilationCache()
		cacheDir = "" // Indicate in-memory fallback
		// Optional: log fallback for debugging
		// fmt.Fprintln(os.Stderr, "WASM cache: in-memory only")
	}

	// Configure go-sqlite3's wazero runtime to use the cache
	sqlite3.RuntimeConfig = wazero.NewRuntimeConfig().WithCompilationCache(cache)

	return cacheDir
}

func init() {
	// Setup WASM compilation cache to avoid 220ms JIT compilation overhead on every process start
	_ = setupWASMCache()
}

// New creates a new SQLite storage backend
func New(ctx context.Context, path string) (*SQLiteStorage, error) {
	// Build connection string with proper URI syntax
	// For :memory: databases, use shared cache so multiple connections see the same data
	var connStr string
	if path == ":memory:" {
		// Use shared in-memory database with a named identifier
		// Note: WAL mode doesn't work with shared in-memory databases, so use DELETE mode
		// The name "memdb" is required for cache=shared to work properly across connections
		connStr = "file:memdb?mode=memory&cache=shared&_pragma=journal_mode(DELETE)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)&_time_format=sqlite"
	} else if strings.HasPrefix(path, "file:") {
		// Already a URI - append our pragmas if not present
		connStr = path
		if !strings.Contains(path, "_pragma=foreign_keys") {
			connStr += "&_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)&_time_format=sqlite"
		}
	} else {
		// Ensure directory exists for file-based databases
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
		// Use file URI with pragmas
		connStr = "file:" + path + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)&_time_format=sqlite"
	}

	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// For all in-memory databases (including file::memory:), force single connection.
	// SQLite's in-memory databases are isolated per connection by default.
	// Without this, different connections in the pool can't see each other's writes (bd-b121, bd-yvlc).
	isInMemory := path == ":memory:" ||
		(strings.HasPrefix(path, "file:") && strings.Contains(path, "mode=memory"))
	if isInMemory {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		// For file-based databases in daemon mode, limit connection pool to prevent
		// connection exhaustion under concurrent load. SQLite WAL mode supports
		// 1 writer + unlimited readers, but we limit to prevent goroutine pile-up
		// on write lock contention (bd-qhws).
		maxConns := runtime.NumCPU() + 1 // 1 writer + N readers
		db.SetMaxOpenConns(maxConns)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(0) // SQLite doesn't need connection recycling
	}

	// For file-based databases, enable WAL mode once after opening the connection.
	if !isInMemory {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
		}
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Initialize schema
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Run all migrations
	if err := RunMigrations(db); err != nil {
		return nil, err
	}

	// Verify schema compatibility after migrations (bd-ckvw)
	// First attempt
	if err := verifySchemaCompatibility(db); err != nil {
		// Schema probe failed - retry migrations once
		if retryErr := RunMigrations(db); retryErr != nil {
			return nil, fmt.Errorf("migration retry failed after schema probe failure: %w (original: %w)", retryErr, err)
		}

		// Probe again after retry
		if err := verifySchemaCompatibility(db); err != nil {
			// Still failing - return fatal error with clear message
			return nil, fmt.Errorf("schema probe failed after migration retry: %w. Database may be corrupted or from incompatible version. Run 'bd doctor' to diagnose", err)
		}
	}

	// Convert to absolute path for consistency (but keep :memory: as-is)
	absPath := path
	if path != ":memory:" {
		var err error
		absPath, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path: %w", err)
		}
	}

	storage := &SQLiteStorage{
		db:     db,
		dbPath: absPath,
	}

	// Hydrate from multi-repo config if configured (bd-307)
	// Skip for in-memory databases (used in tests)
	if path != ":memory:" {
		_, err := storage.HydrateFromMultiRepo(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to hydrate from multi-repo: %w", err)
		}
	}

	return storage, nil
}

// Close closes the database connection.
// It checkpoints the WAL to ensure all writes are flushed to the main database file.
func (s *SQLiteStorage) Close() error {
	s.closed.Store(true)
	// Checkpoint WAL to ensure all writes are persisted to the main database file.
	// Without this, writes may be stranded in the WAL and lost between CLI invocations.
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

// Path returns the absolute path to the database file
func (s *SQLiteStorage) Path() string {
	return s.dbPath
}

// IsClosed returns true if Close() has been called on this storage
func (s *SQLiteStorage) IsClosed() bool {
	return s.closed.Load()
}

// UnderlyingDB returns the underlying *sql.DB connection for extensions.
//
// This allows extensions (like VC) to create their own tables in the same database
// while leveraging the existing connection pool and schema. The returned *sql.DB is
// safe for concurrent use and shares the same transaction isolation and locking
// behavior as the core storage operations.
//
// IMPORTANT SAFETY RULES:
//
// 1. DO NOT call Close() on the returned *sql.DB
//   - The SQLiteStorage owns the connection lifecycle
//   - Closing it will break all storage operations
//   - Use storage.Close() to close the database
//
// 2. DO NOT modify connection pool settings
//   - Avoid SetMaxOpenConns, SetMaxIdleConns, SetConnMaxLifetime, etc.
//   - The storage has already configured these for optimal performance
//
// 3. DO NOT change SQLite PRAGMAs
//   - The database is configured with WAL mode, foreign keys, and busy timeout
//   - Changing these (e.g., journal_mode, synchronous, locking_mode) can cause corruption
//
// 4. Expect errors after storage.Close()
//   - Check storage.IsClosed() before long-running operations if needed
//   - Pass contexts with timeouts to prevent hanging on closed connections
//
// 5. Keep write transactions SHORT
//   - SQLite has a single-writer lock even in WAL mode
//   - Long-running write transactions will block core storage operations
//   - Use read transactions (BEGIN DEFERRED) when possible
//
// GOOD PRACTICES:
//
// - Create extension tables with FOREIGN KEY constraints to maintain referential integrity
// - Use the same DATETIME format (RFC3339 / ISO8601) for consistency
// - Leverage SQLite indexes for query performance
// - Test with -race flag to catch concurrency issues
//
// EXAMPLE (creating a VC extension table):
//
//	db := storage.UnderlyingDB()
//	_, err := db.Exec(`
//	    CREATE TABLE IF NOT EXISTS vc_executions (
//	        id INTEGER PRIMARY KEY AUTOINCREMENT,
//	        issue_id TEXT NOT NULL,
//	        status TEXT NOT NULL,
//	        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
//	        FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
//	    );
//	    CREATE INDEX IF NOT EXISTS idx_vc_executions_issue ON vc_executions(issue_id);
//	`)
func (s *SQLiteStorage) UnderlyingDB() *sql.DB {
	return s.db
}

// UnderlyingConn returns a single connection from the pool for scoped use.
//
// This provides a connection with explicit lifetime boundaries, useful for:
// - One-time DDL operations (CREATE TABLE, ALTER TABLE)
// - Migration scripts that need transaction control
// - Operations that benefit from connection-level state
//
// IMPORTANT: The caller MUST close the connection when done:
//
//	conn, err := storage.UnderlyingConn(ctx)
//	if err != nil {
//	    return err
//	}
//	defer conn.Close()
//
// For general queries and transactions, prefer UnderlyingDB() which manages
// the connection pool automatically.
//
// EXAMPLE (extension table migration):
//
//	conn, err := storage.UnderlyingConn(ctx)
//	if err != nil {
//	    return err
//	}
//	defer conn.Close()
//
//	_, err = conn.ExecContext(ctx, `
//	    CREATE TABLE IF NOT EXISTS vc_executions (
//	        id INTEGER PRIMARY KEY AUTOINCREMENT,
//	        issue_id TEXT NOT NULL,
//	        FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
//	    )
//	`)
func (s *SQLiteStorage) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return s.db.Conn(ctx)
}

// CheckpointWAL checkpoints the WAL file to flush changes to the main database file.
// In WAL mode, writes go to the -wal file, leaving the main .db file untouched.
// Checkpointing:
// - Ensures data persistence by flushing WAL to main database
// - Reduces WAL file size
// - Makes database safe for backup/copy operations
func (s *SQLiteStorage) CheckpointWAL(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(FULL)")
	return err
}
