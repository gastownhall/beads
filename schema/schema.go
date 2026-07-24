// Package schema is the public wrapper around bd's schema migration engine,
// re-exporting the minimal surface external tools need to create or upgrade a
// beads database over a standard database/sql connection.
package schema

import (
	"context"
	"database/sql"

	ischema "github.com/steveyegge/beads/internal/storage/schema"
)

// DBConn is the minimal query interface satisfied by *sql.DB, *sql.Tx, and
// *sql.Conn.
type DBConn = ischema.DBConn

// SchemaSkewError is returned when the database's schema version is ahead of
// the version this module was built against (forward drift).
type SchemaSkewError = ischema.SchemaSkewError

// IsSchemaSkewError reports whether err (or any error it wraps) is a
// *SchemaSkewError.
func IsSchemaSkewError(err error) bool {
	return ischema.IsSchemaSkewError(err)
}

// LatestVersion returns the schema version this module was built against.
func LatestVersion() int {
	return ischema.LatestVersion()
}

// CurrentVersion returns the database's current schema version. A database
// with no schema_migrations table (never migrated) reports version 0.
func CurrentVersion(ctx context.Context, db DBConn) (int, error) {
	return ischema.CurrentVersion(ctx, db)
}

// MigrateUpWithLock applies all pending migrations up to LatestVersion,
// serialized across processes by a per-database Dolt/MySQL named lock. conn
// must be a pinned *sql.Conn because named locks are session-scoped. Returns
// the number of migrations applied (0 when already current).
func MigrateUpWithLock(ctx context.Context, conn *sql.Conn, databaseName string) (applied int, err error) {
	return ischema.MigrateUpWithLock(ctx, conn, databaseName)
}
