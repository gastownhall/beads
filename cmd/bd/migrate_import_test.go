package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
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

// TestMigrateImport_DependencyInsertPlaceholderParity verifies that the
// dependency migration INSERT keeps SQL arity correct when columns change.
func TestMigrateImport_DependencyInsertPlaceholderParity(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "cmd/bd/migrate_import.go")
	data, err := os.ReadFile(path) // #nosec G304 - test reads known source files
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	// Capture INSERT INTO dependencies (...) VALUES (...) in importToDolt.
	re := regexp.MustCompile(`(?s)INSERT INTO dependencies\s*\(\s*(.*?)\)\s*VALUES\s*\(\s*(.*?)\s*\)\s*ON DUPLICATE KEY UPDATE`)
	matches := re.FindSubmatch(data)
	if len(matches) < 3 {
		t.Fatal("failed to locate dependencies INSERT statement in migrate_import.go")
	}

	columnList := strings.TrimSpace(string(matches[1]))
	valuesList := strings.TrimSpace(string(matches[2]))

	columns := splitColumns(columnList)
	placeholders := regexp.MustCompile(`\?`).FindAllString(valuesList, -1)

	if len(columns) != len(placeholders) {
		t.Fatalf("dependencies INSERT arity mismatch: %d columns, %d placeholders\ncolumns=%q\nvalues=%q",
			len(columns), len(placeholders), columnList, valuesList)
	}
}

// TestMigrateImport_IssueInsertPlaceholderParity verifies that issue INSERT
// values keep placeholder count in sync with the column list.
func TestMigrateImport_IssueInsertPlaceholderParity(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "cmd/bd/migrate_import.go")
	data, err := os.ReadFile(path) // #nosec G304 - test reads known source files
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	re := regexp.MustCompile(`(?s)INSERT INTO issues\s*\(\s*(.*?)\)\s*VALUES\s*\(\s*(.*?)\s*\)\s*ON DUPLICATE KEY UPDATE`)
	matches := re.FindSubmatch(data)
	if len(matches) < 3 {
		t.Fatal("failed to locate issues INSERT statement in migrate_import.go")
	}

	columnList := strings.TrimSpace(string(matches[1]))
	valuesList := strings.TrimSpace(string(matches[2]))

	columns := splitColumns(columnList)
	placeholders := regexp.MustCompile(`\?`).FindAllString(valuesList, -1)

	if len(columns) != len(placeholders) {
		t.Fatalf("issues INSERT arity mismatch: %d columns, %d placeholders\ncolumns=%q\nvalues=%q",
			len(columns), len(placeholders), columnList, valuesList)
	}
}

func TestNormalizeDependencyMetadata_JSONSafety(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		want       string
		wantQuoted bool
	}{
		{name: "empty", in: "", want: "{}", wantQuoted: false},
		{name: "whitespace", in: "   ", want: "{}", wantQuoted: false},
		{name: "valid object", in: `{"k":"v"}`, want: `{"k":"v"}`, wantQuoted: false},
		{name: "valid array", in: `[1,2,3]`, want: `[1,2,3]`, wantQuoted: false},
		{name: "plain text", in: "legacy-metadata", want: `"legacy-metadata"`, wantQuoted: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeDependencyMetadata(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeDependencyMetadata(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("normalizeDependencyMetadata(%q) produced invalid JSON: %q", tc.in, got)
			}
			if tc.wantQuoted && !(strings.HasPrefix(got, `"`) && strings.HasSuffix(got, `"`)) {
				t.Fatalf("expected quoted JSON string for %q, got %q", tc.in, got)
			}
		})
	}
}

func TestOrderedIssueIDs_ExcludesOrphanMapKeys(t *testing.T) {
	data := &migrationData{
		issues: []*types.Issue{
			{ID: "bd-2"},
			{ID: "bd-1"},
			{ID: "bd-1"}, // duplicate should be de-duped
		},
		eventsMap: map[string][]*types.Event{
			"bd-2":       {{EventType: types.EventType("commented")}},
			"orphan-evt": {{EventType: types.EventType("commented")}},
		},
		commentsMap: map[string][]*types.Comment{
			"bd-1":       {{IssueID: "bd-1", Text: "ok"}},
			"orphan-cmt": {{IssueID: "orphan-cmt", Text: "orphan"}},
		},
	}

	ids := orderedIssueIDs(data)
	if len(ids) != 2 {
		t.Fatalf("orderedIssueIDs length = %d, want 2", len(ids))
	}
	if ids[0] != "bd-2" || ids[1] != "bd-1" {
		t.Fatalf("orderedIssueIDs = %v, want [bd-2 bd-1]", ids)
	}
}

func TestMigrateImport_ReimportReconcilesIssueAndRelations(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	ctx := context.Background()
	issueID := "test-migrate-reimport-1"
	ts := time.Date(2026, 2, 25, 12, 0, 0, 0, time.UTC)

	dataV1 := &migrationData{
		issues: []*types.Issue{
			{
				ID:                 issueID,
				Title:              "title-v1",
				Description:        "desc-v1",
				Design:             "",
				AcceptanceCriteria: "",
				Notes:              "",
				Status:             types.Status("open"),
				Priority:           2,
				IssueType:          types.IssueType("task"),
				CreatedAt:          ts,
				CreatedBy:          "tester",
				Owner:              "tester",
				UpdatedAt:          ts,
				Labels:             []string{"alpha"},
				Dependencies: []*types.Dependency{
					{
						IssueID:     issueID,
						DependsOnID: "external:rig:dep-v1",
						Type:        types.DependencyType("blocks"),
						CreatedBy:   "tester",
						CreatedAt:   ts,
						Metadata:    `{"v":1}`,
						ThreadID:    "thread-v1",
					},
				},
			},
		},
		eventsMap: map[string][]*types.Event{
			issueID: {
				{
					EventType: types.EventType("commented"),
					Actor:     "actor-v1",
					Comment:   strPtr("event-v1"),
					CreatedAt: ts,
				},
			},
			"orphan-evt": {
				{
					EventType: types.EventType("commented"),
					Actor:     "orphan",
					CreatedAt: ts,
				},
			},
		},
		commentsMap: map[string][]*types.Comment{
			issueID: {
				{
					IssueID:   issueID,
					Author:    "author-v1",
					Text:      "comment-v1",
					CreatedAt: ts,
				},
			},
			"orphan-cmt": {
				{
					IssueID:   "orphan-cmt",
					Author:    "ghost",
					Text:      "ghost-comment",
					CreatedAt: ts,
				},
			},
		},
		config: map[string]string{
			"issue_prefix": "test",
		},
	}

	imported, skipped, err := importToDolt(ctx, store, dataV1)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("first import counts mismatch: imported=%d skipped=%d", imported, skipped)
	}

	dataV2 := &migrationData{
		issues: []*types.Issue{
			{
				ID:                 issueID,
				Title:              "title-v2",
				Description:        "desc-v2",
				Design:             "",
				AcceptanceCriteria: "",
				Notes:              "",
				Status:             types.Status("in_progress"),
				Priority:           1,
				IssueType:          types.IssueType("task"),
				CreatedAt:          ts,
				CreatedBy:          "tester",
				Owner:              "tester",
				UpdatedAt:          ts.Add(1 * time.Hour),
				Labels:             []string{"beta"},
				Dependencies: []*types.Dependency{
					{
						IssueID:     issueID,
						DependsOnID: "external:rig:dep-v2",
						Type:        types.DependencyType("blocks"),
						CreatedBy:   "tester",
						CreatedAt:   ts.Add(1 * time.Hour),
						Metadata:    `{"v":2}`,
						ThreadID:    "thread-v2",
					},
				},
			},
		},
		eventsMap: map[string][]*types.Event{
			issueID: {
				{
					EventType: types.EventType("commented"),
					Actor:     "actor-v2",
					Comment:   strPtr("event-v2"),
					CreatedAt: ts.Add(1 * time.Hour),
				},
			},
		},
		commentsMap: map[string][]*types.Comment{
			issueID: {
				{
					IssueID:   issueID,
					Author:    "author-v2",
					Text:      "comment-v2",
					CreatedAt: ts.Add(1 * time.Hour),
				},
			},
		},
		config: map[string]string{
			"issue_prefix": "test",
		},
	}

	imported, skipped, err = importToDolt(ctx, store, dataV2)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("second import counts mismatch: imported=%d skipped=%d", imported, skipped)
	}

	db := store.UnderlyingDB()

	var title, status string
	if err := db.QueryRowContext(ctx, "SELECT title, status FROM issues WHERE id = ?", issueID).Scan(&title, &status); err != nil {
		t.Fatalf("failed to read issue: %v", err)
	}
	if title != "title-v2" || status != "in_progress" {
		t.Fatalf("issue not updated on re-import: title=%q status=%q", title, status)
	}

	if got := countRowsByIssue(t, ctx, db, "labels", issueID); got != 1 {
		t.Fatalf("labels count = %d, want 1", got)
	}
	if got := countRowsByIssue(t, ctx, db, "dependencies", issueID); got != 1 {
		t.Fatalf("dependencies count = %d, want 1", got)
	}
	if got := countRowsByIssue(t, ctx, db, "events", issueID); got != 1 {
		t.Fatalf("events count = %d, want 1", got)
	}
	if got := countRowsByIssue(t, ctx, db, "comments", issueID); got != 1 {
		t.Fatalf("comments count = %d, want 1", got)
	}

	var label string
	if err := db.QueryRowContext(ctx, "SELECT label FROM labels WHERE issue_id = ?", issueID).Scan(&label); err != nil {
		t.Fatalf("failed to read label: %v", err)
	}
	if label != "beta" {
		t.Fatalf("label mismatch after re-import: got %q want %q", label, "beta")
	}

	var dependsOnID, depMetadata, depThreadID string
	if err := db.QueryRowContext(ctx, "SELECT depends_on_id, metadata, thread_id FROM dependencies WHERE issue_id = ?", issueID).
		Scan(&dependsOnID, &depMetadata, &depThreadID); err != nil {
		t.Fatalf("failed to read dependency: %v", err)
	}
	if dependsOnID != "external:rig:dep-v2" || depThreadID != "thread-v2" {
		t.Fatalf("dependency mismatch after re-import: depends_on=%q thread_id=%q", dependsOnID, depThreadID)
	}
	var depMetaObj map[string]int
	if err := json.Unmarshal([]byte(depMetadata), &depMetaObj); err != nil {
		t.Fatalf("dependency metadata is not valid JSON: %q (%v)", depMetadata, err)
	}
	if depMetaObj["v"] != 2 {
		t.Fatalf("dependency metadata mismatch: %+v", depMetaObj)
	}

	var eventActor, eventComment string
	if err := db.QueryRowContext(ctx, "SELECT actor, comment FROM events WHERE issue_id = ?", issueID).
		Scan(&eventActor, &eventComment); err != nil {
		t.Fatalf("failed to read event: %v", err)
	}
	if eventActor != "actor-v2" || eventComment != "event-v2" {
		t.Fatalf("event mismatch after re-import: actor=%q comment=%q", eventActor, eventComment)
	}

	var commentAuthor, commentText string
	if err := db.QueryRowContext(ctx, "SELECT author, text FROM comments WHERE issue_id = ?", issueID).
		Scan(&commentAuthor, &commentText); err != nil {
		t.Fatalf("failed to read comment: %v", err)
	}
	if commentAuthor != "author-v2" || commentText != "comment-v2" {
		t.Fatalf("comment mismatch after re-import: author=%q text=%q", commentAuthor, commentText)
	}

	if got := countRowsByIssue(t, ctx, db, "events", "orphan-evt"); got != 0 {
		t.Fatalf("orphan events should not be imported, got %d rows", got)
	}
	if got := countRowsByIssue(t, ctx, db, "comments", "orphan-cmt"); got != 0 {
		t.Fatalf("orphan comments should not be imported, got %d rows", got)
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

func splitColumns(raw string) []string {
	parts := strings.Split(raw, ",")
	columns := make([]string, 0, len(parts))
	for _, p := range parts {
		col := strings.TrimSpace(strings.Trim(p, "`"))
		if col != "" {
			columns = append(columns, col)
		}
	}
	return columns
}

func countRowsByIssue(t *testing.T, ctx context.Context, db *sql.DB, table, issueID string) int {
	t.Helper()

	var query string
	switch table {
	case "labels":
		query = "SELECT COUNT(*) FROM labels WHERE issue_id = ?"
	case "dependencies":
		query = "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?"
	case "events":
		query = "SELECT COUNT(*) FROM events WHERE issue_id = ?"
	case "comments":
		query = "SELECT COUNT(*) FROM comments WHERE issue_id = ?"
	default:
		t.Fatalf("unsupported table in test helper: %s", table)
	}

	var count int
	if err := db.QueryRowContext(ctx, query, issueID).Scan(&count); err != nil {
		t.Fatalf("failed to count rows for table=%s issue_id=%s: %v", table, issueID, err)
	}
	return count
}

func strPtr(s string) *string {
	return &s
}
