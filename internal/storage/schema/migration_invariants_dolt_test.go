package schema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// TestGH5269MigrationMarkRequiresAppliedDDL reproduces the reported invariant
// violation against an externally supplied Dolt 2.2.x sql-server. The normal
// test suite skips it when no external DSN is supplied; unit tests cover the
// invariant decision and version-recording boundary without Docker.
func TestGH5269MigrationMarkRequiresAppliedDDL(t *testing.T) {
	adminDSN := os.Getenv("BEADS_GH5269_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set BEADS_GH5269_ADMIN_DSN to run the GH#5269 reproduction")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	dbName := fmt.Sprintf("gh5269_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+dbName+"`")
	})

	dsn := adminDSN + dbName + "?multiStatements=true&parseTime=true"
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()

	if _, err := MigrateUp(ctx, conn); err != nil {
		t.Fatalf("construct current schema: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())"); err != nil {
		t.Fatalf("construct reported schema drift: %v", err)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_ADD('dependencies')"); err != nil {
		t.Fatalf("stage reported schema drift: %v", err)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', 'reproduce GH#5269 schema drift')"); err != nil {
		t.Fatalf("commit reported schema drift: %v", err)
	}

	// This is the reporter's permanent state: the cursor is current but one of
	// the DDL effects it claims is absent. A normal migration pass must not
	// silently accept that contradiction.
	_, migrateErr := MigrateUp(ctx, conn)

	var version int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read migration mark: %v", err)
	}
	var columnDefault sql.NullString
	if err := conn.QueryRowContext(ctx, `
SELECT COLUMN_DEFAULT
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`).Scan(&columnDefault); err != nil {
		t.Fatalf("read dependencies.id default: %v", err)
	}
	var tableName, createSQL string
	if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE dependencies").Scan(&tableName, &createSQL); err != nil {
		t.Fatalf("show dependencies DDL: %v", err)
	}
	if columnDefault.Valid && !strings.Contains(strings.ToLower(createSQL), "default (uuid())") {
		t.Fatalf("INFORMATION_SCHEMA reports default %q but SHOW CREATE TABLE disagrees: %s", columnDefault.String, createSQL)
	}

	if migrateErr == nil && version == LatestVersion() && columnDefault.Valid {
		t.Fatalf("migration reported success at v%d, but dependencies.id default remains %q", version, columnDefault.String)
	}
	if migrateErr != nil {
		t.Fatalf("migration consistency pass returned an error instead of repairing: %v", migrateErr)
	}
	if version != LatestVersion() {
		t.Fatalf("schema version = %d, want %d", version, LatestVersion())
	}
	if columnDefault.Valid {
		t.Fatalf("dependencies.id default = %q, want NULL", columnDefault.String)
	}
	var dirtyDependencies int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'dependencies'").Scan(&dirtyDependencies); err != nil {
		t.Fatalf("read dependencies dolt_status: %v", err)
	}
	if dirtyDependencies != 0 {
		t.Fatalf("dependencies remains dirty after invariant repair: dolt_status count = %d", dirtyDependencies)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_RESET('--hard')"); err != nil {
		t.Fatalf("reset working set to HEAD: %v", err)
	}
	columnDefault = sql.NullString{}
	if err := conn.QueryRowContext(ctx, `
SELECT COLUMN_DEFAULT
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`).Scan(&columnDefault); err != nil {
		t.Fatalf("read dependencies.id default after hard reset: %v", err)
	}
	if columnDefault.Valid {
		t.Fatalf("dependencies.id default after hard reset = %q, repair was not committed to HEAD", columnDefault.String)
	}
}
