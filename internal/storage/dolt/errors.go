package dolt

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// Sentinel errors for the dolt storage layer.
// These complement the storage-level sentinels (storage.ErrNotFound, etc.)
// with dolt-specific error types.
var (
	// ErrTransaction indicates a transaction begin/commit/rollback failure.
	ErrTransaction = errors.New("transaction error")

	// ErrQuery indicates a database query failure.
	ErrQuery = errors.New("query error")

	// ErrScan indicates a failure scanning database rows into Go values.
	ErrScan = errors.New("scan error")

	// ErrExec indicates a database exec (INSERT/UPDATE/DELETE) failure.
	ErrExec = errors.New("exec error")
)

// isTableNotExistError returns true if the error indicates a MySQL/Dolt
// "table doesn't exist" error (error 1146). Used to distinguish legitimate
// fallthrough (pre-migration databases without wisps table) from real errors
// (timeouts, connection failures, corrupt data).
func isTableNotExistError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1146
}

// isSerializationError returns true if the error is a Dolt/MySQL serialization
// failure that guarantees the transaction was rolled back. Safe to retry.
//   - 1213 (ER_LOCK_DEADLOCK): concurrent transactions conflict at commit time
//   - 1205 (ER_LOCK_WAIT_TIMEOUT): lock wait exceeded, transaction rolled back
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}

// wrapDBError wraps a database error with operation context.
// If err is sql.ErrNoRows, it is converted to storage.ErrNotFound.
// If err is nil, nil is returned.
func wrapDBError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// wrapTransactionError wraps a transaction error with operation context.
func wrapTransactionError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrTransaction, err)
}

// wrapScanError wraps a row scan error with operation context.
func wrapScanError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrScan, err)
}

// wrapQueryError wraps a query error with operation context.
func wrapQueryError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrQuery, err)
}

// wrapExecError wraps an exec error with operation context.
func wrapExecError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrExec, err)
}

// databaseNotFoundError builds the "database not found" error with a config-aware
// hint about sync.git-remote and backup recovery. Extracted from openServerConnection
// for testability.
func databaseNotFoundError(cfg *Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "database %q not found on Dolt server at %s:%d\n\n", cfg.Database, cfg.ServerHost, cfg.ServerPort)

	// Check if backup files exist — strong signal this is a branch-switch or
	// fresh-clone scenario rather than a server misconfiguration (GH#2327).
	if HasBackupFiles(cfg.BeadsDir) {
		b.WriteString("Backup files found in .beads/backup/ — this may be a branch-switch\n")
		b.WriteString("or fresh-clone scenario where the Dolt database doesn't exist yet.\n\n")
		b.WriteString("Use the safe entry point for existing-project recovery:\n")
		b.WriteString("  bd bootstrap                 # Auto-detect remote/backup/JSONL recovery or initialization\n")
		b.WriteString("  bd backup restore            # If backup recovery still needs manual restore\n\n")
		b.WriteString("If this is NOT a branch switch, see common causes below.\n\n")
	}

	b.WriteString("Common causes:\n")
	b.WriteString("  - Switched git branches (the Dolt database is runtime state, not in git)\n")
	b.WriteString("  - The server is serving a different data directory than expected\n")
	b.WriteString("  - The server was restarted and is using a different port\n")
	b.WriteString("  - Another project's Dolt server is running on this port\n\n")
	b.WriteString("To diagnose:\n")
	b.WriteString("  bd doctor                  # Check server and database health\n")
	b.WriteString("  bd dolt status             # Show which data directory the server is using")

	if cfg.SyncGitRemote != "" {
		fmt.Fprintf(&b, "\n\nTip: sync.git-remote is configured (%s).\nRun bd bootstrap to recover from the remote or confirm what bootstrap will do with --dry-run.", cfg.SyncGitRemote)
	} else {
		b.WriteString("\n\nTip: If this is an existing project, fresh clone, or shared-server recovery, run bd bootstrap first.\n")
		b.WriteString("If bootstrap cannot find the expected remote automatically, set sync.git-remote\nin .beads/config.yaml and re-run bd bootstrap.\n")
		b.WriteString("Use bd bootstrap --dry-run if you need to confirm the plan before it initializes anything.\n")
		b.WriteString("Use bd init only when creating a brand-new project with no existing .beads data.")
	}

	return errors.New(b.String())
}

// HasBackupFiles checks whether .beads/backup/ contains any JSONL files,
// indicating a prior backup that could be restored (GH#2327).
func HasBackupFiles(beadsDir string) bool {
	if beadsDir == "" {
		return false
	}
	backupDir := filepath.Join(beadsDir, "backup")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}
