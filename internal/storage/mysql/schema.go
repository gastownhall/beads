package mysql

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ddl is the MySQL backend schema, embedded from schema.sql. It is the Dolt
// `SHOW CREATE TABLE` output at HEAD (all migrations applied) with the handful of
// Dolt-vs-MySQL-8 fixes required by stock MySQL: TEXT/JSON literal defaults →
// expression form `DEFAULT (”)`, the overlong `spec_id` index → a 191-char prefix
// (InnoDB's 3072-byte key cap), and the one-target CHECK constraints dropped (MySQL 8
// forbids a column in both a CHECK and an FK cascade; issueops enforces the invariant
// in code). No data-plane translation is needed — bd's canonical SQL is MySQL.
//
//go:embed schema.sql
var ddl string

const (
	schemaVersion    = "1"
	schemaVersionKey = "mysql_schema_version"
)

// identRe restricts a database name to a plain identifier so it can be safely
// interpolated into CREATE DATABASE (which cannot take a placeholder).
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// InitSchema creates the workspace database if absent (via a server connection with
// no default database), applies the DDL to it, seeds the default config rows on first
// provision, and stamps the schema version. baseDSN is the server DSN; database is the
// per-workspace database (MySQL's unit of isolation, analogous to a Postgres schema).
func InitSchema(ctx context.Context, baseDSN, database string) error {
	if !identRe.MatchString(database) {
		return fmt.Errorf("mysql: invalid database name %q", database)
	}
	serverDSN, err := withDatabase(baseDSN, "")
	if err != nil {
		return fmt.Errorf("mysql: parse DSN: %w", err)
	}
	srv, err := sql.Open("mysql", serverDSN)
	if err != nil {
		return fmt.Errorf("mysql: open server: %w", err)
	}
	if _, err := srv.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+database+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"); err != nil {
		_ = srv.Close()
		return fmt.Errorf("mysql: create database %q: %w", database, err)
	}
	_ = srv.Close()

	wsDSN, err := withDatabase(baseDSN, database)
	if err != nil {
		return fmt.Errorf("mysql: parse DSN: %w", err)
	}
	db, err := sql.Open("mysql", wsDSN)
	if err != nil {
		return fmt.Errorf("mysql: open workspace db: %w", err)
	}
	defer db.Close()
	for _, stmt := range splitStatements(ddl) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("mysql: exec DDL statement: %w\n-- statement:\n%s", err, stmt)
		}
	}
	fresh, err := stampSchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if fresh {
		if err := seedDefaultConfig(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

// defaultConfigSeeds mirrors migration 0016 exactly — the config rows a fresh Dolt
// workspace materializes on init. Seeded only on first provision (like the one-shot
// migration), so a `config unset` is never resurrected on the next open.
var defaultConfigSeeds = [][2]string{
	{"compaction_enabled", "false"},
	{"compact_tier1_days", "30"},
	{"compact_tier1_dep_levels", "2"},
	{"compact_tier2_days", "90"},
	{"compact_tier2_dep_levels", "5"},
	{"compact_tier2_commits", "100"},
	{"compact_batch_size", "50"},
	{"compact_parallel_workers", "5"},
	{"auto_compact_enabled", "false"},
}

func seedDefaultConfig(ctx context.Context, db *sql.DB) error {
	for _, kv := range defaultConfigSeeds {
		if _, err := db.ExecContext(ctx,
			"INSERT IGNORE INTO config (`key`, `value`) VALUES (?, ?)", kv[0], kv[1]); err != nil {
			return fmt.Errorf("mysql: seed default config %q: %w", kv[0], err)
		}
	}
	return nil
}

// stampSchemaVersion records schemaVersion in the metadata table on first provision
// and refuses to open a workspace written by a binary with a different schema version
// (the proof-wedge has no migrator). Returns fresh=true only on the first stamp.
func stampSchemaVersion(ctx context.Context, db *sql.DB) (fresh bool, err error) {
	var stored string
	err = db.QueryRowContext(ctx, "SELECT `value` FROM metadata WHERE `key` = ?", schemaVersionKey).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Provision runs on every open; concurrent first opens of the same
		// workspace database can both reach here. INSERT IGNORE lets both
		// succeed; only the opener that inserted the row (RowsAffected==1) is
		// fresh and seeds defaults (seedDefaultConfig is already INSERT IGNORE).
		res, ierr := db.ExecContext(ctx, "INSERT IGNORE INTO metadata (`key`, `value`) VALUES (?, ?)", schemaVersionKey, schemaVersion)
		if ierr != nil {
			return false, fmt.Errorf("mysql: stamp schema version: %w", ierr)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return true, nil
		}
		// Lost the race: re-read what the winner stored and version-check it.
		if rerr := db.QueryRowContext(ctx, "SELECT `value` FROM metadata WHERE `key` = ?", schemaVersionKey).Scan(&stored); rerr != nil {
			return false, fmt.Errorf("mysql: read schema version after conflict: %w", rerr)
		}
		if stored != schemaVersion {
			return false, fmt.Errorf("mysql: workspace schema version %s, this binary requires %s — no migrator in the proof-wedge, recreate the workspace or use a matching binary", stored, schemaVersion)
		}
		return false, nil
	case err != nil:
		return false, fmt.Errorf("mysql: read schema version: %w", err)
	case stored != schemaVersion:
		return false, fmt.Errorf("mysql: workspace schema version %s, this binary requires %s — no migrator in the proof-wedge, recreate the workspace or use a matching binary", stored, schemaVersion)
	default:
		return false, nil
	}
}

// splitStatements splits the DDL on ';' at statement boundaries. The MySQL schema has
// no procedural bodies (triggers/procedures) or embedded ';' — the CHECK constraints
// that would complicate this were removed — so a plain split suffices. Blank fragments
// (and SQL line comments) are dropped.
func splitStatements(ddl string) []string {
	var stmts []string
	for _, raw := range strings.Split(ddl, ";") {
		var b strings.Builder
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}
