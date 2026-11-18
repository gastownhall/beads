package dolt

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestFailureCloseReasonSQLMatchesIsFailureClose executes the generated
// failure predicate against a real close_reason column and requires the
// engine's answer to equal types.IsFailureClose for every keyword, mixed case,
// apostrophes, Unicode case folding, substring false positives, empty text and
// NULL (read as empty).
func TestFailureCloseReasonSQLMatchesIsFailureClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "cbsql-row")

	reasons := []sql.NullString{{}} // NULL
	add := func(s string) { reasons = append(reasons, sql.NullString{String: s, Valid: true}) }
	for _, keyword := range types.FailureCloseKeywords {
		add(keyword)
		add(strings.ToUpper(keyword))
		add("step " + keyword + " upstream")
	}
	for _, s := range []string{
		"", "Closed", "done", "completed successfully", "Won't Fix", "WONT FIX", "wontfix",
		"duplicate of bd-1", "superseded by bd-2", "Fixed error message", "Timed out",
		"fáiled", "ÉRROR", "Canceled by user", "unblocked", "bloc\u212Aed", "ABORTED!",
		"rejected; won''t fix",
	} {
		add(s)
	}

	query := "SELECT " + issueops.FailureCloseReasonSQL("t.close_reason") + " FROM issues t WHERE t.id = ?"
	for _, reason := range reasons {
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET close_reason = ? WHERE id = ?", reason, "cbsql-row"); err != nil {
			t.Fatalf("set close_reason %q: %v", reason.String, err)
		}
		var got bool
		if err := store.db.QueryRowContext(ctx, query, "cbsql-row").Scan(&got); err != nil {
			t.Fatalf("evaluate predicate for %q (valid=%v): %v", reason.String, reason.Valid, err)
		}
		if want := types.IsFailureClose(reason.String); got != want {
			t.Errorf("SQL failure predicate(%q, valid=%v) = %v, IsFailureClose = %v", reason.String, reason.Valid, got, want)
		}
	}
}
