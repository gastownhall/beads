package dolt

import (
	"database/sql"
	"errors"
	"fmt"
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
// failure (Error 1213). This occurs when concurrent transactions conflict at
// commit time; the caller should retry the transaction.
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
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
// hint about sync.git-remote. Extracted from openServerConnection for testability.
func databaseNotFoundError(cfg *Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "database %q not found on Dolt server at %s:%d\n\n", cfg.Database, cfg.ServerHost, cfg.ServerPort)
	b.WriteString("This usually means a server configuration problem, NOT a missing database.\n")
	b.WriteString("Common causes:\n")
	b.WriteString("  - The server is serving a different data directory than expected\n")
	b.WriteString("  - The server was restarted and is using a different port\n")
	b.WriteString("  - Another project's Dolt server is running on this port\n\n")
	b.WriteString("To diagnose:\n")
	b.WriteString("  bd doctor                  # Check server and database health\n")
	b.WriteString("  bd dolt status             # Show which data directory the server is using")

	if cfg.SyncGitRemote != "" {
		fmt.Fprintf(&b, "\n\nTip: sync.git-remote is configured (%s).\nRun bd init to bootstrap from the remote.", cfg.SyncGitRemote)
	} else {
		b.WriteString("\n\nTip: To bootstrap from an existing Dolt remote, set sync.git-remote\nin .beads/config.yaml and re-run bd init.")
	}

	return errors.New(b.String())
}
