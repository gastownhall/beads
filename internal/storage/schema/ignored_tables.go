package schema

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"
)

// ignoredMigration identifies an embedded .up.sql migration (or a subset of
// its statements) that defines or alters a dolt-ignored table.
type ignoredMigration struct {
	version int
	// filter, if non-empty, selects only statements containing this substring
	// (case-insensitive). When empty, the entire migration file is used.
	filter string
}

// ignoredMigrations lists the migrations that define or alter dolt-ignored
// tables, in the order they must be applied. This replaces the former
// hand-maintained Go constants — the .up.sql files are the single source
// of truth.
var ignoredMigrations = []ignoredMigration{
	{version: 29},                  // CREATE TABLE local_metadata
	{version: 11},                  // CREATE TABLE repo_mtimes
	{version: 20},                  // CREATE TABLE wisps
	{version: 21},                  // CREATE TABLE wisp_labels, wisp_dependencies, wisp_events, wisp_comments
	{version: 22},                  // CREATE INDEX on wisp_dependencies
	{version: 23, filter: "wisps"}, // ALTER TABLE wisps ADD COLUMN no_history (skip issues ALTER)
	{version: 27, filter: "wisps"}, // ALTER TABLE wisps ADD COLUMN started_at (skip issues ALTER)
	{version: 31},                  // CREATE INDEX idx_wisp_events_created_at
}

var (
	ignoredDDLOnce sync.Once
	ignoredDDLVal  []string
)

// IgnoredTableDDL returns the ordered list of SQL statements needed to
// recreate all dolt-ignored tables from scratch. Derived from embedded
// migration files at first call and cached thereafter.
func IgnoredTableDDL() []string {
	ignoredDDLOnce.Do(func() {
		ignoredDDLVal = buildIgnoredTableDDL()
	})
	return ignoredDDLVal
}

func buildIgnoredTableDDL() []string {
	var result []string
	for _, im := range ignoredMigrations {
		raw := ReadMigrationSQL(im.version)
		stmts := splitStatements(raw)
		if im.filter != "" {
			filterLower := strings.ToLower(im.filter)
			for _, s := range stmts {
				if strings.Contains(strings.ToLower(s), filterLower) {
					result = append(result, s)
				}
			}
		} else {
			result = append(result, stmts...)
		}
	}
	return result
}

// ReadMigrationSQL reads the embedded .up.sql file for the given version number
// and returns its contents as a string. Panics if the migration is not found.
func ReadMigrationSQL(version int) string {
	entries, err := fs.ReadDir(upMigrations, "migrations")
	if err != nil {
		panic(fmt.Sprintf("schema: reading migrations dir: %v", err))
	}
	prefix := fmt.Sprintf("%04d_", version)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".up.sql") {
			data, err := upMigrations.ReadFile("migrations/" + e.Name())
			if err != nil {
				panic(fmt.Sprintf("schema: reading migration %s: %v", e.Name(), err))
			}
			return string(data)
		}
	}
	panic(fmt.Sprintf("schema: migration %04d not found", version))
}

// splitStatements splits SQL text on semicolons into individual statements,
// stripping SQL comments and whitespace. Returns only non-empty statements.
func splitStatements(sql string) []string {
	raw := strings.Split(sql, ";")
	var out []string
	for _, s := range raw {
		s = stripSQLComments(s)
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// stripSQLComments removes lines starting with -- from SQL text.
func stripSQLComments(sql string) string {
	var lines []string
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
