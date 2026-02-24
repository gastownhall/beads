package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestMigrateImport_ColumnParityWithInsertIssue verifies that the migration
// INSERT in importToDolt covers the same columns as the canonical insertIssue
// helper in internal/storage/dolt/issues.go. This catches silent data loss
// when new columns are added to the schema but not to the migration path.
func TestMigrateImport_ColumnParityWithInsertIssue(t *testing.T) {
	root := moduleRoot(t)
	migrateColumns := extractInsertColumns(t, filepath.Join(root, "cmd/bd/migrate_import.go"))
	canonicalColumns := extractInsertColumns(t, filepath.Join(root, "internal/storage/dolt/issues.go"))

	if len(migrateColumns) == 0 {
		t.Fatal("failed to extract columns from migrate_import.go")
	}
	if len(canonicalColumns) == 0 {
		t.Fatal("failed to extract columns from issues.go")
	}

	// Sort both for comparison
	sort.Strings(migrateColumns)
	sort.Strings(canonicalColumns)

	// Find columns in canonical but missing from migrate
	missing := []string{}
	migrateSet := make(map[string]bool)
	for _, col := range migrateColumns {
		migrateSet[col] = true
	}
	for _, col := range canonicalColumns {
		if !migrateSet[col] {
			missing = append(missing, col)
		}
	}

	if len(missing) > 0 {
		t.Errorf("migrate_import.go INSERT is missing columns present in insertIssue: %v\n"+
			"migrate has %d columns, canonical has %d columns",
			missing, len(migrateColumns), len(canonicalColumns))
	}
}

// moduleRoot finds the Go module root by walking up from the test file.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find module root (no go.mod)")
		}
		dir = parent
	}
}

// extractInsertColumns reads a Go source file and extracts column names from
// the first INSERT INTO issues (...) statement.
func extractInsertColumns(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 - test reads known source files
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	// Match INSERT INTO issues ( ... ) — capture the column list
	re := regexp.MustCompile(`(?s)INSERT INTO issues\s*\(\s*(.*?)\)\s*VALUES`)
	matches := re.FindSubmatch(data)
	if len(matches) < 2 {
		return nil
	}

	raw := string(matches[1])
	// Split on commas and clean up
	parts := strings.Split(raw, ",")
	columns := make([]string, 0, len(parts))
	for _, p := range parts {
		col := strings.TrimSpace(p)
		col = strings.Trim(col, "`")
		if col != "" {
			columns = append(columns, col)
		}
	}
	return columns
}
