//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
)

//go:embed schema/*.up.sql
var upMigrations embed.FS

// migrateUp applies all embedded .up.sql migrations that haven't been applied yet.
func migrateUp(ctx context.Context, tx *sql.Tx) error {
	// Bootstrap the tracking table.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Find the current version.
	var current int
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current)
	if err != nil {
		return fmt.Errorf("reading current migration version: %w", err)
	}

	// Collect and sort migration files.
	entries, err := fs.ReadDir(upMigrations, "schema")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
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
			return fmt.Errorf("parsing migration filename %q: %w", e.Name(), err)
		}
		if v > current {
			pending = append(pending, migrationFile{version: v, name: e.Name()})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	if len(pending) == 0 {
		return nil
	}

	// Apply each pending migration.
	for _, mf := range pending {
		data, err := upMigrations.ReadFile("schema/" + mf.name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", mf.name, err)
		}

		for _, stmt := range splitStatements(string(data)) {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" || isOnlyComments(stmt) {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				// DOLT_COMMIT "nothing to commit" is expected and benign.
				if strings.Contains(strings.ToLower(stmt), "dolt_commit") &&
					strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
					continue
				}
				return fmt.Errorf("migration %s failed: %w\nStatement: %s", mf.name, err, truncateForError(stmt))
			}
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", mf.version); err != nil {
			return fmt.Errorf("recording migration %s: %w", mf.name, err)
		}
	}

	log.Printf("embeddeddolt: applied %d migration(s) (version %d → %d)",
		len(pending), current, pending[len(pending)-1].version)
	return nil
}

// parseVersion extracts the leading integer from a migration filename like "0001_create_issues.up.sql".
func parseVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no version prefix")
	}
	return strconv.Atoi(parts[0])
}

// splitStatements splits a SQL script into individual statements on ";".
func splitStatements(script string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	stringChar := byte(0)

	for i := 0; i < len(script); i++ {
		c := script[i]

		if inString {
			current.WriteByte(c)
			if c == stringChar && (i == 0 || script[i-1] != '\\') {
				inString = false
			}
			continue
		}

		if c == '\'' || c == '"' || c == '`' {
			inString = true
			stringChar = c
			current.WriteByte(c)
			continue
		}

		if c == ';' {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteByte(c)
	}

	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

func truncateForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func isOnlyComments(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return false
	}
	return true
}
