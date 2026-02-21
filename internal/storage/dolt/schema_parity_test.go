//go:build cgo

package dolt

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// columnInfo holds the subset of information_schema.columns needed for parity checks.
type columnInfo struct {
	Name       string
	ColumnType string // e.g. "varchar(255)", "text", "int"
}

// queryColumns returns the column names and types for a table, ordered by ordinal position.
func queryColumns(t *testing.T, store *DoltStore, table string) []columnInfo {
	t.Helper()
	rows, err := store.db.Query(`
		SELECT column_name, column_type
		FROM information_schema.columns
		WHERE table_name = ? AND table_schema = DATABASE()
		ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("failed to query columns for %s: %v", table, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var ci columnInfo
		if err := rows.Scan(&ci.Name, &ci.ColumnType); err != nil {
			t.Fatalf("failed to scan column info for %s: %v", table, err)
		}
		cols = append(cols, ci)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error for %s: %v", table, err)
	}
	return cols
}

// columnNames extracts just the names from a slice of columnInfo, sorted.
func columnNames(cols []columnInfo) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	sort.Strings(names)
	return names
}

// TestSchemaParityIssuesVsWisps verifies that the wisps table has exactly the
// same columns (by name and type) as the issues table. This catches schema drift
// if columns are added to issues but not to wisps (or vice versa).
func TestSchemaParityIssuesVsWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	issuesCols := queryColumns(t, store, "issues")
	wispsCols := queryColumns(t, store, "wisps")

	if len(issuesCols) == 0 {
		t.Fatal("issues table has no columns — schema not initialized?")
	}
	if len(wispsCols) == 0 {
		t.Fatal("wisps table has no columns — migration 004 not run?")
	}

	// Build maps for comparison
	issuesMap := make(map[string]string, len(issuesCols))
	for _, c := range issuesCols {
		issuesMap[c.Name] = c.ColumnType
	}
	wispsMap := make(map[string]string, len(wispsCols))
	for _, c := range wispsCols {
		wispsMap[c.Name] = c.ColumnType
	}

	// Check for columns in issues but missing from wisps
	var missing []string
	for name := range issuesMap {
		if _, ok := wispsMap[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("columns in issues but missing from wisps: %s", strings.Join(missing, ", "))
	}

	// Check for columns in wisps but missing from issues
	var extra []string
	for name := range wispsMap {
		if _, ok := issuesMap[name]; !ok {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("columns in wisps but missing from issues: %s", strings.Join(extra, ", "))
	}

	// Check type mismatches for shared columns
	for name, issueType := range issuesMap {
		if wispType, ok := wispsMap[name]; ok && issueType != wispType {
			t.Errorf("column %q type mismatch: issues=%q, wisps=%q", name, issueType, wispType)
		}
	}
}

// TestSchemaParityAuxiliaryTables verifies that wisp auxiliary tables have the
// same column names as their issues counterparts. Type/nullability differences
// are allowed (wisps are more permissive), but column names must match.
func TestSchemaParityAuxiliaryTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	pairs := []struct {
		issueTable string
		wispTable  string
	}{
		{"labels", "wisp_labels"},
		{"dependencies", "wisp_dependencies"},
		{"events", "wisp_events"},
		{"comments", "wisp_comments"},
	}

	for _, pair := range pairs {
		t.Run(fmt.Sprintf("%s_vs_%s", pair.issueTable, pair.wispTable), func(t *testing.T) {
			issueCols := queryColumns(t, store, pair.issueTable)
			wispCols := queryColumns(t, store, pair.wispTable)

			if len(issueCols) == 0 {
				t.Fatalf("%s table has no columns", pair.issueTable)
			}
			if len(wispCols) == 0 {
				t.Fatalf("%s table has no columns — migration 005 not run?", pair.wispTable)
			}

			issueNames := columnNames(issueCols)
			wispNames := columnNames(wispCols)

			// Column names must be identical
			issueSet := make(map[string]bool, len(issueNames))
			for _, n := range issueNames {
				issueSet[n] = true
			}
			wispSet := make(map[string]bool, len(wispNames))
			for _, n := range wispNames {
				wispSet[n] = true
			}

			for _, n := range issueNames {
				if !wispSet[n] {
					t.Errorf("column %q in %s but missing from %s", n, pair.issueTable, pair.wispTable)
				}
			}
			for _, n := range wispNames {
				if !issueSet[n] {
					t.Errorf("column %q in %s but missing from %s", n, pair.wispTable, pair.issueTable)
				}
			}
		})
	}
}

// TestMigrations004And005Together verifies that migrations 004 (wisps table)
// and 005 (wisp auxiliary tables) run correctly in sequence. Migration 005
// depends on 004's "wisp_%" dolt_ignore pattern to keep auxiliary tables
// out of Dolt history.
func TestMigrations004And005Together(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// After setupTestStore, both migrations have already run via initSchemaOnDB.
	// Verify all expected tables exist.
	expectedTables := []string{"wisps", "wisp_labels", "wisp_dependencies", "wisp_events", "wisp_comments"}
	for _, table := range expectedTables {
		var count int
		err := store.db.QueryRow(`
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_name = ? AND table_schema = DATABASE()`, table).Scan(&count)
		if err != nil {
			t.Fatalf("failed to check table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s should exist after migrations 004+005", table)
		}
	}

	// Verify dolt_ignore patterns cover all wisp tables
	var patternCount int
	err := store.db.QueryRow("SELECT COUNT(*) FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')").Scan(&patternCount)
	if err != nil {
		t.Fatalf("failed to query dolt_ignore: %v", err)
	}
	if patternCount != 2 {
		t.Errorf("expected 2 dolt_ignore patterns (wisps, wisp_%%), got %d", patternCount)
	}

	// Verify none of the wisp tables are staged after dolt_add
	_, err = store.db.Exec("CALL DOLT_ADD('-A')")
	if err != nil {
		t.Fatalf("dolt_add failed: %v", err)
	}

	for _, table := range expectedTables {
		var staged bool
		err := store.db.QueryRow("SELECT staged FROM dolt_status WHERE table_name = ?", table).Scan(&staged)
		if err == nil && staged {
			t.Errorf("table %s should NOT be staged (dolt_ignore should prevent staging)", table)
		}
	}
}
