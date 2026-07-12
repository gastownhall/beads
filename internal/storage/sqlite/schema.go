package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

// ddl is the SQLite backend schema, embedded from schema.sql. It is the Dolt/MySQL
// schema with SQLite adjustments: ENGINE/CHARSET/COLLATE stripped, inline KEY/UNIQUE
// KEY lifted to CREATE INDEX (SQLite forbids inline non-PK indexes), ON UPDATE
// CURRENT_TIMESTAMP dropped (unsupported), json → text, and prefix-index lengths
// removed (SQLite has no InnoDB key-length cap). Backticks, `?` placeholders, and
// tinyint(1) all work natively.
//
//go:embed schema.sql
var ddl string

const (
	schemaVersion    = "1"
	schemaVersionKey = "sqlite_schema_version"
)

// InitSchema applies the DDL, seeds the default config rows on first provision, and
// stamps the schema version. db is a raw modernc connection (the DDL and seeds are
// native SQLite and need no dialect translation).
func InitSchema(ctx context.Context, db *sql.DB) error {
	for _, stmt := range splitStatements(ddl) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite: exec DDL statement: %w\n-- statement:\n%s", err, stmt)
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

// defaultConfigSeeds mirrors migration 0016 exactly. Seeded only on first provision.
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
			"INSERT OR IGNORE INTO config (`key`, `value`) VALUES (?, ?)", kv[0], kv[1]); err != nil {
			return fmt.Errorf("sqlite: seed default config %q: %w", kv[0], err)
		}
	}
	return nil
}

// stampSchemaVersion records schemaVersion on first provision and refuses a mismatched
// workspace (no migrator in the wedge). Returns fresh=true only on the first stamp.
func stampSchemaVersion(ctx context.Context, db *sql.DB) (fresh bool, err error) {
	var stored string
	err = db.QueryRowContext(ctx, "SELECT `value` FROM metadata WHERE `key` = ?", schemaVersionKey).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Provision runs on every open; two bd processes opening a not-yet-
		// provisioned workspace concurrently can both reach here. INSERT OR
		// IGNORE lets both succeed; only the opener that inserted the row
		// (RowsAffected==1) is fresh and seeds defaults.
		res, ierr := db.ExecContext(ctx, "INSERT OR IGNORE INTO metadata (`key`, `value`) VALUES (?, ?)", schemaVersionKey, schemaVersion)
		if ierr != nil {
			return false, fmt.Errorf("sqlite: stamp schema version: %w", ierr)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return true, nil
		}
		// Lost the race: re-read what the winner stored and version-check it.
		if rerr := db.QueryRowContext(ctx, "SELECT `value` FROM metadata WHERE `key` = ?", schemaVersionKey).Scan(&stored); rerr != nil {
			return false, fmt.Errorf("sqlite: read schema version after conflict: %w", rerr)
		}
		if stored != schemaVersion {
			return false, fmt.Errorf("sqlite: workspace schema version %s, this binary requires %s — recreate the workspace or use a matching binary", stored, schemaVersion)
		}
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sqlite: read schema version: %w", err)
	case stored != schemaVersion:
		return false, fmt.Errorf("sqlite: workspace schema version %s, this binary requires %s — recreate the workspace or use a matching binary", stored, schemaVersion)
	default:
		return false, nil
	}
}

// splitStatements splits the DDL on ';' at statement boundaries; the schema has no
// procedural bodies or embedded ';'. Blank fragments and line comments are dropped.
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
