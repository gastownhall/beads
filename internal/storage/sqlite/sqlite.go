// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	// Import SQLite driver
	sqlite3 "github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/internal/types"
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
			return nil, fmt.Errorf("migration retry failed after schema probe failure: %w (original: %v)", retryErr, err)
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

// REMOVED (bd-8e05): getNextIDForPrefix and AllocateNextID - sequential ID generation
// no longer needed with hash-based IDs
// Migration functions moved to migrations.go (bd-fc2d, bd-b245)

// getNextChildNumber atomically generates the next child number for a parent ID
// Uses the child_counters table for atomic, cross-process child ID generation
// Hash ID generation functions moved to hash_ids.go (bd-90a5)

// REMOVED (bd-c7af): SyncAllCounters - no longer needed with hash IDs

// REMOVED (bd-166): derivePrefixFromPath was causing duplicate issues with wrong prefix
// The database should ALWAYS have issue_prefix config set explicitly (by 'bd init' or auto-import)
// Never derive prefix from filename - it leads to silent data corruption

// CreateIssue creates a new issue
func (s *SQLiteStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Validate issue before creating
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Set timestamps
	now := time.Now()
	issue.CreatedAt = now
	issue.UpdatedAt = now

	// Compute content hash (bd-95)
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Acquire a dedicated connection for the transaction.
	// This is necessary because we need to execute raw SQL ("BEGIN IMMEDIATE", "COMMIT")
	// on the same connection, and database/sql's connection pool would otherwise
	// use different connections for different queries.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Start IMMEDIATE transaction to acquire write lock early and prevent race conditions.
	// IMMEDIATE acquires a RESERVED lock immediately, preventing other IMMEDIATE or EXCLUSIVE
	// transactions from starting. This serializes ID generation across concurrent writers.
	//
	// We use raw Exec instead of BeginTx because database/sql doesn't support transaction
	// modes in BeginTx, and modernc.org/sqlite's BeginTx always uses DEFERRED mode.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	// Track commit state for defer cleanup
	// Use context.Background() for ROLLBACK to ensure cleanup happens even if ctx is canceled
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Get prefix from config (needed for both ID generation and validation)
	var prefix string
	err = conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		// This prevents duplicate issues with wrong prefix
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate or validate ID
	if issue.ID == "" {
		// Generate hash-based ID with adaptive length based on database size (bd-ea2a13)
		generatedID, err := GenerateIssueID(ctx, conn, prefix, issue, actor)
		if err != nil {
			return err
		}
		issue.ID = generatedID
	} else {
		// Validate that explicitly provided ID matches the configured prefix (bd-177)
		if err := ValidateIssueIDPrefix(issue.ID, prefix); err != nil {
			return err
		}

		// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
		if strings.Contains(issue.ID, ".") {
			// Try to resurrect entire parent chain if any parents are missing
			// Use the conn-based version to participate in the same transaction
			resurrected, err := s.tryResurrectParentChainWithConn(ctx, conn, issue.ID)
			if err != nil {
				return fmt.Errorf("failed to resurrect parent chain for %s: %w", issue.ID, err)
			}
			if !resurrected {
				// Parent(s) not found in JSONL history - cannot proceed
				lastDot := strings.LastIndex(issue.ID, ".")
				parentID := issue.ID[:lastDot]
				return fmt.Errorf("parent issue %s does not exist and could not be resurrected from JSONL history", parentID)
			}
		}
	}

	// Insert issue
	if err := insertIssue(ctx, conn, issue); err != nil {
		return err
	}

	// Record creation event
	if err := recordCreatedEvent(ctx, conn, issue, actor); err != nil {
		return err
	}

	// Mark issue as dirty for incremental export
	if err := markDirty(ctx, conn, issue.ID); err != nil {
		return err
	}

	// Commit the transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}

// validateBatchIssues validates all issues in a batch and sets timestamps
// Batch operation functions moved to batch_ops.go (bd-c796)

// GetIssue retrieves an issue by ID
func (s *SQLiteStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var issue types.Issue
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRef sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var sourceRepo sql.NullString

	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if externalRef.Valid {
		issue.ExternalRef = &externalRef.String
	}
	if compactedAt.Valid {
		issue.CompactedAt = &compactedAt.Time
	}
	if compactedAtCommit.Valid {
		issue.CompactedAtCommit = &compactedAtCommit.String
	}
	if originalSize.Valid {
		issue.OriginalSize = int(originalSize.Int64)
	}
	if sourceRepo.Valid {
		issue.SourceRepo = sourceRepo.String
	}

	// Fetch labels for this issue
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return &issue, nil
}

// GetIssueByExternalRef retrieves an issue by external reference
func (s *SQLiteStorage) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var issue types.Issue
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRefCol sql.NullString
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64
	var contentHash sql.NullString
	var compactedAtCommit sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size
		FROM issues
		WHERE external_ref = ?
	`, externalRef).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRefCol,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue by external_ref: %w", err)
	}

	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if externalRefCol.Valid {
		issue.ExternalRef = &externalRefCol.String
	}
	if compactedAt.Valid {
		issue.CompactedAt = &compactedAt.Time
	}
	if compactedAtCommit.Valid {
		issue.CompactedAtCommit = &compactedAtCommit.String
	}
	if originalSize.Valid {
		issue.OriginalSize = int(originalSize.Int64)
	}

	// Fetch labels for this issue
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

	return &issue, nil
}

// Allowed fields for update to prevent SQL injection
var allowedUpdateFields = map[string]bool{
	"status":              true,
	"priority":            true,
	"title":               true,
	"assignee":            true,
	"description":         true,
	"design":              true,
	"acceptance_criteria": true,
	"notes":               true,
	"issue_type":          true,
	"estimated_minutes":   true,
	"external_ref":        true,
	"closed_at":           true,
}

// validatePriority validates a priority value
// Validation functions moved to validators.go (bd-d9e0)

// determineEventType determines the event type for an update based on old and new status
func determineEventType(oldIssue *types.Issue, updates map[string]interface{}) types.EventType {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return types.EventUpdated
	}

	newStatus, ok := statusVal.(string)
	if !ok {
		return types.EventUpdated
	}

	if newStatus == string(types.StatusClosed) {
		return types.EventClosed
	}
	if oldIssue.Status == types.StatusClosed {
		return types.EventReopened
	}
	return types.EventStatusChanged
}

// manageClosedAt automatically manages the closed_at field based on status changes
func manageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]

	// If closed_at is explicitly provided in updates, it's already in setClauses/args
	// and we should not override it (important for import operations that preserve timestamps)
	_, hasExplicitClosedAt := updates["closed_at"]
	if hasExplicitClosedAt {
		return setClauses, args
	}

	if !hasStatus {
		return setClauses, args
	}

	// Handle both string and types.Status
	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusClosed) {
		// Changing to closed: ensure closed_at is set
		now := time.Now()
		updates["closed_at"] = now
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, now)
	} else if oldIssue.Status == types.StatusClosed {
		// Changing from closed to something else: clear closed_at
		updates["closed_at"] = nil
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, nil)
	}

	return setClauses, args
}

// UpdateIssue updates fields on an issue
func (s *SQLiteStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Get old issue for event
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return err
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	// Build update query with validated field names
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now()}

	for key, value := range updates {
		// Prevent SQL injection by validating field names
		if !allowedUpdateFields[key] {
			return fmt.Errorf("invalid field for update: %s", key)
		}

		// Validate field values
		if err := validateFieldUpdate(key, value); err != nil {
			return err
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", key))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes (enforce invariant)
	setClauses, args = manageClosedAt(oldIssue, updates, setClauses, args)

	// Recompute content_hash if any content fields changed (bd-95)
	contentChanged := false
	contentFields := []string{"title", "description", "design", "acceptance_criteria", "notes", "status", "priority", "issue_type", "assignee", "external_ref"}
	for _, field := range contentFields {
		if _, exists := updates[field]; exists {
			contentChanged = true
			break
		}
	}
	if contentChanged {
		// Get updated issue to compute hash
		updatedIssue := *oldIssue
		for key, value := range updates {
			switch key {
			case "title":
				updatedIssue.Title = value.(string)
			case "description":
				updatedIssue.Description = value.(string)
			case "design":
				updatedIssue.Design = value.(string)
			case "acceptance_criteria":
				updatedIssue.AcceptanceCriteria = value.(string)
			case "notes":
				updatedIssue.Notes = value.(string)
			case "status":
				// Handle both string and types.Status
				if s, ok := value.(types.Status); ok {
					updatedIssue.Status = s
				} else {
					updatedIssue.Status = types.Status(value.(string))
				}
			case "priority":
				updatedIssue.Priority = value.(int)
			case "issue_type":
				// Handle both string and types.IssueType
				if t, ok := value.(types.IssueType); ok {
					updatedIssue.IssueType = t
				} else {
					updatedIssue.IssueType = types.IssueType(value.(string))
				}
			case "assignee":
				if value == nil {
					updatedIssue.Assignee = ""
				} else {
					updatedIssue.Assignee = value.(string)
				}
			case "external_ref":
				if value == nil {
					updatedIssue.ExternalRef = nil
				} else {
					// Handle both string and *string
					switch v := value.(type) {
					case string:
						updatedIssue.ExternalRef = &v
					case *string:
						updatedIssue.ExternalRef = v
					default:
						return fmt.Errorf("external_ref must be string or *string, got %T", value)
					}
				}
			}
		}
		newHash := updatedIssue.ComputeContentHash()
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, newHash)
	}

	args = append(args, id)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update issue
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", ")) // #nosec G201 - safe SQL with controlled column names
	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event
	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		oldData = []byte(fmt.Sprintf(`{"id":"%s"}`, id))
	}
	newData, err := json.Marshal(updates)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		newData = []byte(`{}`)
	}
	oldDataStr := string(oldData)
	newDataStr := string(newData)

	eventType := determineEventType(oldIssue, updates)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, id, eventType, actor, oldDataStr, newDataStr)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache if status changed (bd-5qim)
	// Status changes affect which issues are blocked (blockers must be open/in_progress/blocked)
	if _, statusChanged := updates["status"]; statusChanged {
		if err := s.invalidateBlockedCache(ctx, tx); err != nil {
			return fmt.Errorf("failed to invalidate blocked cache: %w", err)
		}
	}

	return tx.Commit()
}

// UpdateIssueID updates an issue ID and all its text fields in a single transaction
func (s *SQLiteStorage) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	// Get exclusive connection to ensure PRAGMA applies
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Disable foreign keys on this specific connection
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign keys: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, time.Now(), oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue ID: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", oldID)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update comments: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE dirty_issues SET issue_id = ? WHERE issue_id = ?
	`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update dirty_issues: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, newID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record rename event: %w", err)
	}

	return tx.Commit()
}

// RenameDependencyPrefix updates the prefix in all dependency records
func (s *SQLiteStorage) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}

// RenameCounterPrefix is a no-op with hash-based IDs (bd-8e05)
// Kept for backward compatibility with rename-prefix command
func (s *SQLiteStorage) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	// Hash-based IDs don't use counters, so nothing to update
	return nil
}

// ResetCounter is a no-op with hash-based IDs (bd-8e05)
// Kept for backward compatibility
func (s *SQLiteStorage) ResetCounter(ctx context.Context, prefix string) error {
	// Hash-based IDs don't use counters, so nothing to reset
	return nil
}

// CloseIssue closes an issue with a reason
func (s *SQLiteStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	now := time.Now()

	// Update with special event handling
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to close issue: %w", err)
	}
	
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, types.EventClosed, actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache since status changed to closed (bd-5qim)
	// Closed issues don't block others, so this affects blocking calculations
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	return tx.Commit()
}

// DeleteIssue permanently removes an issue from the database
func (s *SQLiteStorage) DeleteIssue(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete dependencies (both directions)
	_, err = tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// Delete events
	_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete events: %w", err)
	}

	// Delete from dirty_issues
	_, err = tx.ExecContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete dirty marker: %w", err)
	}

	// Delete the issue itself
	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// REMOVED (bd-c7af): Counter sync after deletion - no longer needed with hash IDs
	return nil
}

// DeleteIssuesResult contains statistics about a batch deletion operation
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}

// DeleteIssues deletes multiple issues in a single transaction
// If cascade is true, recursively deletes dependents
// If cascade is false but force is true, deletes issues and orphans their dependents
// If cascade and force are both false, returns an error if any issue has dependents
// If dryRun is true, only computes statistics without deleting
func (s *SQLiteStorage) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &DeleteIssuesResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	idSet := buildIDSet(ids)
	result := &DeleteIssuesResult{}

	expandedIDs, err := s.resolveDeleteSet(ctx, tx, ids, idSet, cascade, force, result)
	if err != nil {
		return nil, err
	}

	inClause, args := buildSQLInClause(expandedIDs)
	if err := s.populateDeleteStats(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if dryRun {
		return result, nil
	}

	if err := s.executeDelete(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// REMOVED (bd-c7af): Counter sync after deletion - no longer needed with hash IDs

	return result, nil
}

func buildIDSet(ids []string) map[string]bool {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	return idSet
}

func (s *SQLiteStorage) resolveDeleteSet(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, cascade bool, force bool, result *DeleteIssuesResult) ([]string, error) {
	if cascade {
		return s.expandWithDependents(ctx, tx, ids, idSet)
	}
	if !force {
		return ids, s.validateNoDependents(ctx, tx, ids, idSet, result)
	}
	return ids, s.trackOrphanedIssues(ctx, tx, ids, idSet, result)
}

func (s *SQLiteStorage) expandWithDependents(ctx context.Context, tx *sql.Tx, ids []string, _ map[string]bool) ([]string, error) {
	allToDelete, err := s.findAllDependentsRecursive(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to find dependents: %w", err)
	}
	expandedIDs := make([]string, 0, len(allToDelete))
	for id := range allToDelete {
		expandedIDs = append(expandedIDs, id)
	}
	return expandedIDs, nil
}

func (s *SQLiteStorage) validateNoDependents(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	for _, id := range ids {
		if err := s.checkSingleIssueValidation(ctx, tx, id, idSet, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStorage) checkSingleIssueValidation(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, result *DeleteIssuesResult) error {
	var depCount int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE depends_on_id = ?`, id).Scan(&depCount)
	if err != nil {
		return fmt.Errorf("failed to check dependents for %s: %w", id, err)
	}
	if depCount == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	hasExternal := false
	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			hasExternal = true
			result.OrphanedIssues = append(result.OrphanedIssues, depID)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dependents for %s: %w", id, err)
	}

	if hasExternal {
		return fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
	}
	return nil
}

func (s *SQLiteStorage) trackOrphanedIssues(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	orphanSet := make(map[string]bool)
	for _, id := range ids {
		if err := s.collectOrphansForID(ctx, tx, id, idSet, orphanSet); err != nil {
			return err
		}
	}
	for orphanID := range orphanSet {
		result.OrphanedIssues = append(result.OrphanedIssues, orphanID)
	}
	return nil
}

func (s *SQLiteStorage) collectOrphansForID(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, orphanSet map[string]bool) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			orphanSet[depID] = true
		}
	}
	return rows.Err()
}

func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *SQLiteStorage) populateDeleteStats(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	counts := []struct {
		query string
		dest  *int
	}{
		{fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), &result.DependenciesCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, inClause), &result.LabelsCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, inClause), &result.EventsCount},
	}

	for _, c := range counts {
		queryArgs := args
		if c.dest == &result.DependenciesCount {
			queryArgs = append(args, args...)
		}
		if err := tx.QueryRowContext(ctx, c.query, queryArgs...).Scan(c.dest); err != nil {
			return fmt.Errorf("failed to count: %w", err)
		}
	}

	result.DeletedCount = len(args)
	return nil
}

func (s *SQLiteStorage) executeDelete(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	deletes := []struct {
		query string
		args  []interface{}
	}{
		{fmt.Sprintf(`DELETE FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), append(args, args...)},
		{fmt.Sprintf(`DELETE FROM labels WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM events WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM dirty_issues WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM issues WHERE id IN (%s)`, inClause), args},
	}

	for i, d := range deletes {
		execResult, err := tx.ExecContext(ctx, d.query, d.args...)
		if err != nil {
			return fmt.Errorf("failed to delete: %w", err)
		}
		if i == len(deletes)-1 {
			rowsAffected, err := execResult.RowsAffected()
			if err != nil {
				return fmt.Errorf("failed to check rows affected: %w", err)
			}
			result.DeletedCount = int(rowsAffected)
		}
	}
	return nil
}

// findAllDependentsRecursive finds all issues that depend on the given issues, recursively
func (s *SQLiteStorage) findAllDependentsRecursive(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		current := toProcess[0]
		toProcess = toProcess[1:]

		rows, err := tx.QueryContext(ctx,
			`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, current)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				return nil, err
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// SearchIssues finds issues matching query and filters
func (s *SQLiteStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		pattern := "%" + filter.TitleSearch + "%"
		args = append(args, pattern)
	}

	// Pattern matching
	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "notes LIKE ?")
		args = append(args, "%"+filter.NotesContains+"%")
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	// Priority ranges
	if filter.PriorityMin != nil {
		whereClauses = append(whereClauses, "priority >= ?")
		args = append(args, *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		whereClauses = append(whereClauses, "priority <= ?")
		args = append(args, *filter.PriorityMax)
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Date ranges
	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at > ?")
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at < ?")
		args = append(args, filter.CreatedBefore.Format(time.RFC3339))
	}
	if filter.UpdatedAfter != nil {
		whereClauses = append(whereClauses, "updated_at > ?")
		args = append(args, filter.UpdatedAfter.Format(time.RFC3339))
	}
	if filter.UpdatedBefore != nil {
		whereClauses = append(whereClauses, "updated_at < ?")
		args = append(args, filter.UpdatedBefore.Format(time.RFC3339))
	}
	if filter.ClosedAfter != nil {
		whereClauses = append(whereClauses, "closed_at > ?")
		args = append(args, filter.ClosedAfter.Format(time.RFC3339))
	}
	if filter.ClosedBefore != nil {
		whereClauses = append(whereClauses, "closed_at < ?")
		args = append(args, filter.ClosedBefore.Format(time.RFC3339))
	}

	// Empty/null checks
	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	}
	if filter.NoLabels {
		whereClauses = append(whereClauses, "id NOT IN (SELECT DISTINCT issue_id FROM labels)")
	}

	// Label filtering: issue must have ALL specified labels
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Label filtering (OR): issue must have AT LEAST ONE of these labels
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}

	// ID filtering: match specific issue IDs
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, filter.Limit)
	}

	// #nosec G201 - safe SQL with controlled formatting
	querySQL := fmt.Sprintf(`
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref, source_repo
		FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}

// SetConfig sets a configuration value
func (s *SQLiteStorage) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetConfig gets a configuration value
func (s *SQLiteStorage) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// GetAllConfig gets all configuration key-value pairs
func (s *SQLiteStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		config[key] = value
	}
	return config, rows.Err()
}

// DeleteConfig deletes a configuration value
func (s *SQLiteStorage) DeleteConfig(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return err
}

// GetOrphanHandling gets the import.orphan_handling config value
// Returns OrphanAllow (the default) if not set or if value is invalid
func (s *SQLiteStorage) GetOrphanHandling(ctx context.Context) OrphanHandling {
	value, err := s.GetConfig(ctx, "import.orphan_handling")
	if err != nil || value == "" {
		return OrphanAllow // Default
	}

	switch OrphanHandling(value) {
	case OrphanStrict, OrphanResurrect, OrphanSkip, OrphanAllow:
		return OrphanHandling(value)
	default:
		return OrphanAllow // Invalid value, use default
	}
}

// SetMetadata sets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) SetMetadata(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetMetadata gets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// AddIssueComment adds a comment to an issue
func (s *SQLiteStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	// Verify issue exists
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, issueID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	// Insert comment
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, issueID, author, text)
	if err != nil {
		return nil, fmt.Errorf("failed to insert comment: %w", err)
	}

	// Get the inserted comment ID
	commentID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment ID: %w", err)
	}

	// Fetch the complete comment
	comment := &types.Comment{}
	err = s.db.QueryRowContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments WHERE id = ?
	`, commentID).Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comment: %w", err)
	}

	// Mark issue as dirty for JSONL export
	if err := s.MarkIssueDirty(ctx, issueID); err != nil {
		return nil, fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return comment, nil
}

// GetIssueComments retrieves all comments for an issue
func (s *SQLiteStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to query comments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var comments []*types.Comment
	for rows.Next() {
		comment := &types.Comment{}
		err := rows.Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		comments = append(comments, comment)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating comments: %w", err)
	}

	return comments, nil
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	s.closed.Store(true)
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
