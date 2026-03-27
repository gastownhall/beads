package embeddeddolt

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed schema/*.up.sql
var upMigrations embed.FS

var (
	latestOnce sync.Once
	latestVer  int
)

// latestVersion returns the highest version number among the embedded .up.sql files.
// Computed once and cached.
func latestVersion() int {
	latestOnce.Do(func() {
		entries, err := fs.ReadDir(upMigrations, "schema")
		if err != nil {
			panic(fmt.Sprintf("embeddeddolt: failed to read embedded schema migrations: %v", err))
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
				continue
			}
			v, err := parseVersion(e.Name())
			if err != nil {
				panic(fmt.Sprintf("embeddeddolt: invalid migration filename %q: %v", e.Name(), err))
			}
			if v > latestVer {
				latestVer = v
			}
		}
	})
	return latestVer
}

// parseVersion extracts the leading integer from a migration filename like "0001_create_issues.up.sql".
func parseVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no version prefix")
	}
	return strconv.Atoi(parts[0])
}

// migrateUp applies all embedded .up.sql migrations that haven't been applied yet.
// Returns the number of migrations applied.
func migrateUp(ctx context.Context, tx *sql.Tx) (int, error) {
	// Bootstrap the tracking table.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return 0, fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Find the current version.
	var current int
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current)
	if err == sql.ErrNoRows {
		current = 0
	} else if err != nil {
		return 0, fmt.Errorf("reading current migration version: %w", err)
	}

	// Fast path: if current version matches the highest embedded migration, nothing to do.
	if current >= latestVersion() {
		return 0, nil
	}

	// If schema_migrations is empty but core tables already exist (e.g. restored
	// from a DoltStore backup that doesn't track embedded migrations), backfill
	// all versions so we don't re-run migrations that would fail on "already exists".
	if current == 0 {
		var tableCount int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'issues' AND table_schema = DATABASE()").Scan(&tableCount); err == nil && tableCount > 0 {
			return backfillMigrations(ctx, tx)
		}
	}

	// Collect and sort migration files.
	entries, err := fs.ReadDir(upMigrations, "schema")
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations: %w", err)
	}

	type migrationFile struct {
		version int
		name    string
	}

	var pending []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, err := parseVersion(e.Name())
		if err != nil {
			return 0, fmt.Errorf("parsing migration filename %q: %w", e.Name(), err)
		}
		if v > current {
			pending = append(pending, migrationFile{version: v, name: e.Name()})
		}
	}

	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	if len(pending) == 0 {
		return 0, nil
	}

	// Apply each pending migration. The DSN has multiStatements=true,
	// so each file is executed as a single ExecContext call.
	for _, mf := range pending {
		data, err := upMigrations.ReadFile("schema/" + mf.name)
		if err != nil {
			return 0, fmt.Errorf("reading migration %s: %w", mf.name, err)
		}

		sql := strings.TrimSpace(string(data))
		if sql != "" {
			if _, err := tx.ExecContext(ctx, sql); err != nil {
				return 0, fmt.Errorf("migration %s failed: %w", mf.name, err)
			}
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", mf.version); err != nil {
			return 0, fmt.Errorf("recording migration %s: %w", mf.name, err)
		}
	}

	return len(pending), nil
}

// backfillMigrations marks all known migrations as applied without executing them.
// Used when a database is restored from a backup that predates the schema_migrations
// tracking table — the schema is already correct, we just need to record that fact.
func backfillMigrations(ctx context.Context, tx *sql.Tx) (int, error) {
	entries, err := fs.ReadDir(upMigrations, "schema")
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations for backfill: %w", err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, err := parseVersion(e.Name())
		if err != nil {
			return 0, fmt.Errorf("parsing migration filename %q: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO schema_migrations (version) VALUES (?)", v); err != nil {
			return 0, fmt.Errorf("backfilling migration %s: %w", e.Name(), err)
		}
		n++
	}
	return n, nil
}
