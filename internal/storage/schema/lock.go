package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

const (
	migrationLockPrefix        = "bd_schema_init:"
	migrationLockNameMaxLength = 64
	// Keep this below the server-mode callers' retry budgets so a contended
	// lock wait can time out and still leave room for a real retry.
	migrationLockAcquireTimeoutSeconds = 5
	migrationLockCleanupTimeout        = 5 * time.Second
)

var (
	// ErrMigrationLockUnavailable marks transient failures to acquire the
	// per-database migration lock.
	ErrMigrationLockUnavailable = errors.New("schema migration lock unavailable")

	// ErrMigrationLockRelease marks uncertain lock cleanup after migrations.
	ErrMigrationLockRelease = errors.New("schema migration lock release failed")
)

// IsMigrationLockError reports whether err came from migration lock acquisition
// or cleanup and should be treated as retryable by callers with a retry budget.
func IsMigrationLockError(err error) bool {
	return errors.Is(err, ErrMigrationLockUnavailable) || errors.Is(err, ErrMigrationLockRelease)
}

// MigrationLockName returns a Dolt/MySQL named-lock key within the documented
// 64-byte limit while preserving readable names when they already fit.
func MigrationLockName(databaseName string) string {
	raw := migrationLockPrefix + databaseName
	if len(raw) <= migrationLockNameMaxLength {
		return raw
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(databaseName))
	return fmt.Sprintf("%s%016x", migrationLockPrefix, h.Sum64())
}

// MigrateUpWithLock serializes schema migrations for a single Dolt sql-server
// database. conn must be a pinned *sql.Conn because MySQL/Dolt named locks are
// session-scoped; GET_LOCK, migrations, and RELEASE_LOCK must run on the same
// server session. Embedded Dolt intentionally uses bare MigrateUp because it
// relies on the embedded driver's file/concurrency controls instead of
// sql-server session locks.
func MigrateUpWithLock(ctx context.Context, conn *sql.Conn, databaseName string) (applied int, err error) {
	lockName := MigrationLockName(databaseName)
	if err := AcquireMigrationLock(ctx, conn, lockName); err != nil {
		return 0, err
	}
	defer func() {
		if releaseErr := ReleaseMigrationLock(conn, lockName); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()

	return MigrateUp(ctx, conn)
}

// AcquireMigrationLock acquires the named schema migration lock on the pinned
// connection's current Dolt/MySQL session.
func AcquireMigrationLock(ctx context.Context, conn *sql.Conn, lockName string) error {
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, migrationLockAcquireTimeoutSeconds).Scan(&locked); err != nil {
		return fmt.Errorf("schema: acquire migration lock: %w: %w", ErrMigrationLockUnavailable, err)
	}
	if !locked.Valid {
		return fmt.Errorf("schema: acquire migration lock: %w: returned NULL", ErrMigrationLockUnavailable)
	}
	if locked.Int64 != 1 {
		return fmt.Errorf("schema: acquire migration lock: %w: timeout", ErrMigrationLockUnavailable)
	}
	return nil
}

// ReleaseMigrationLock releases the named schema migration lock from the same
// pinned Dolt/MySQL session used to acquire it.
func ReleaseMigrationLock(conn *sql.Conn, lockName string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockCleanupTimeout)
	defer cancel()

	var released sql.NullInt64
	if err := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: %w", ErrMigrationLockRelease, err)
	}
	if !released.Valid {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: returned NULL", ErrMigrationLockRelease)
	}
	if released.Int64 != 1 {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: returned %d", ErrMigrationLockRelease, released.Int64)
	}
	return nil
}

func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(driverConn any) error {
		return driver.ErrBadConn
	})
}
