package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// cmtIDs projects comment IDs in result order.
func cmtIDs(cs []*types.Comment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func cmtEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// seedGroupedComments inserts n comments on issueID with ids that sort lexically
// by index and created_at bucketed into same-second groups of groupSize (forcing
// the (created_at, id) tie-break). It returns the whole-second base time. Raw
// INSERTs give the test full control of both id and created_at.
func seedGroupedComments(t *testing.T, ctx context.Context, db *sql.DB, issueID string, n, groupSize int) time.Time {
	t.Helper()
	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		at := base.Add(time.Duration(i/groupSize) * time.Second)
		if _, err := db.ExecContext(ctx,
			"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (?, ?, ?, ?, ?)",
			id, issueID, "tester", fmt.Sprintf("c%d", i), at); err != nil {
			t.Fatalf("insert comment %d: %v", i, err)
		}
	}
	return base
}

// walkCommentsPage pages the whole thread with the given limit, feeding each
// page's last (created_at, id) back in as the cursor, and returns the collected
// ids. It fails on any duplicate id or an over-long page.
func walkCommentsPage(t *testing.T, ctx context.Context, s *DoltStore, issueID string, limit int) []string {
	t.Helper()
	var collected []string
	seen := map[string]bool{}
	var after storage.CommentPageCursor
	for i := 0; i < 1000; i++ {
		page, err := s.GetIssueCommentsPage(ctx, issueID, after, limit)
		if err != nil {
			t.Fatalf("GetIssueCommentsPage(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > limit {
			t.Fatalf("page %d size = %d, want <= %d", i, len(page), limit)
		}
		for _, c := range page {
			if seen[c.ID] {
				t.Fatalf("duplicate id %q across pages — same-second overflow was lost", c.ID)
			}
			seen[c.ID] = true
			collected = append(collected, c.ID)
		}
		last := page[len(page)-1]
		after = storage.CommentPageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return collected
}

// TestGetIssueCommentsPageWalkEqualsFullRead is the money property: a keyset walk
// with a page size smaller than a same-second group reproduces GetIssueComments
// exactly — same order, no dropped or duplicated comment, no gap — even across
// same-second collisions where a created_at-only cursor would lose the overflow.
func TestGetIssueCommentsPageWalkEqualsFullRead(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "cm-page", Title: "cm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	// 25 comments in 5 same-second groups of 5 (groupSize 5 > page size 4).
	const n, groupSize, pageSize = 25, 5, 4
	seedGroupedComments(t, ctx, store.db, iss.ID, n, groupSize)

	full, err := store.GetIssueComments(ctx, iss.ID)
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(full) != n {
		t.Fatalf("full read = %d comments, want %d", len(full), n)
	}

	// Same-second tie-break, explicit: the first group shares one created_at and
	// is returned in strictly ascending id order (the secondary sort key).
	for i := 1; i < groupSize; i++ {
		if !full[i].CreatedAt.Equal(full[0].CreatedAt) {
			t.Fatalf("group 0 not same-second: full[%d].CreatedAt=%v != full[0]=%v", i, full[i].CreatedAt, full[0].CreatedAt)
		}
		if full[i].ID <= full[i-1].ID {
			t.Fatalf("same-second group not id-ascending at %d: %q <= %q", i, full[i].ID, full[i-1].ID)
		}
	}

	// The walk (page size < group size) equals the full read exactly.
	walked := walkCommentsPage(t, ctx, store, iss.ID, pageSize)
	if want := cmtIDs(full); !cmtEqual(walked, want) {
		t.Fatalf("keyset walk = %v,\nwant full read %v", walked, want)
	}

	// First page (zero cursor) is exactly the first pageSize in order.
	first, err := store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{}, pageSize)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(first): %v", err)
	}
	if got, want := cmtIDs(first), cmtIDs(full)[:pageSize]; !cmtEqual(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}

	// A cursor planted on the last comment (past-the-end) yields an empty page.
	lastC := full[len(full)-1]
	end, err := store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{CreatedAt: lastC.CreatedAt, ID: lastC.ID}, pageSize)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(past-the-end): %v", err)
	}
	if len(end) != 0 {
		t.Fatalf("past-the-end page = %v, want empty", cmtIDs(end))
	}

	// limit <= 0 falls back to the store default (>= n here), returning the whole
	// thread in one page identical to the full read.
	def, err := store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{}, 0)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(limit=0): %v", err)
	}
	if got, want := cmtIDs(def), cmtIDs(full); !cmtEqual(got, want) {
		t.Fatalf("default-limit page = %v, want full read %v", got, want)
	}
}

// TestGetIssueCommentsPageEmptyThread pins the empty-thread and missing-issue
// semantics: both return an empty page with no error, matching GetIssueComments.
func TestGetIssueCommentsPageEmptyThread(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "cm-empty", Title: "cm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	empty, err := store.GetIssueCommentsPage(ctx, iss.ID, storage.CommentPageCursor{}, 10)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(empty thread): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty thread page = %v, want empty", cmtIDs(empty))
	}

	missing, err := store.GetIssueCommentsPage(ctx, "does-not-exist", storage.CommentPageCursor{}, 10)
	if err != nil {
		t.Fatalf("GetIssueCommentsPage(missing issue): %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing-issue page = %v, want empty", cmtIDs(missing))
	}
}

// TestGetIssueCommentsPageWisp verifies the wisp route: a page on an active wisp
// reads wisp_comments (not comments) and matches GetIssueComments for that wisp.
func TestGetIssueCommentsPageWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{ID: "cm-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	base := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	for i := 0; i < 6; i++ {
		if _, err := store.ImportIssueComment(ctx, wisp.ID, "tester", fmt.Sprintf("w%d", i), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("import wisp comment %d: %v", i, err)
		}
	}

	// Nothing landed in the durable comments table — routing hit wisp_comments.
	var durable int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", wisp.ID).Scan(&durable); err != nil {
		t.Fatalf("count durable comments: %v", err)
	}
	if durable != 0 {
		t.Fatalf("wisp comments leaked into durable table: %d rows", durable)
	}

	full, err := store.GetIssueComments(ctx, wisp.ID)
	if err != nil {
		t.Fatalf("GetIssueComments(wisp): %v", err)
	}
	if len(full) != 6 {
		t.Fatalf("wisp full read = %d, want 6", len(full))
	}
	walked := walkCommentsPage(t, ctx, store, wisp.ID, 2)
	if want := cmtIDs(full); !cmtEqual(walked, want) {
		t.Fatalf("wisp keyset walk = %v, want %v", walked, want)
	}
}

// TestGetIssueCommentsPagePlanIsIndexed is the sargability regression guard: the
// keyset page predicate must seek the (issue_id, created_at, id) index
// (IndexedTableAccess), not full-scan-and-filter the issue's comments. The
// redundant `created_at >= ?` lower bound in CommentsKeysetPredicate is what keeps
// the Dolt planner on the index. It single-sources the guarded SQL from
// production (issueops.CommentsPageQuery, which embeds CommentsKeysetPredicate)
// and skips rather than fails if the EXPLAIN format is unrecognizable.
func TestGetIssueCommentsPagePlanIsIndexed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "cm-plan", Title: "cm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	seedGroupedComments(t, ctx, store.db, iss.ID, 10, 2)

	// Single-source: the production query string must embed the exported
	// predicate, so a drift in CommentsKeysetPredicate breaks both.
	prod := issueops.CommentsPageQuery("comments", true, 100)
	if !strings.Contains(prod, issueops.CommentsKeysetPredicate) {
		t.Fatalf("production page query does not embed CommentsKeysetPredicate:\n%s", prod)
	}

	const cur = "2024-01-02 03:04:05"
	// Four ? placeholders: issue_id, created_at (lower bound), created_at
	// (strict), id (tie-break).
	plan := explainPlan(t, ctx, store.db, literalizeParams(prod, "'"+iss.ID+"'", "'"+cur+"'", "'"+cur+"'", "''"))

	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	// Pin the COMPOSITE index specifically, not merely "some index". Dolt's
	// EXPLAIN identifies an index by its column list rather than its name, so the
	// composite idx_comments_issue_created_id shows as
	// [comments.issue_id,comments.created_at,comments.id]. The single-column
	// idx_comments_issue shows only [comments.issue_id] (verified by a
	// drop-index probe during authoring) — so this exact signature fails if the
	// planner regresses to the issue-only index (leaving the created_at range +
	// id tie-break to a filter+sort) or to a full scan.
	const compositeIndexCols = "[comments.issue_id,comments.created_at,comments.id]"
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, compositeIndexCols) {
		t.Fatalf("comment page predicate does not seek the composite index (want IndexedTableAccess on %s) — regressed to the issue-only index or a full Table scan.\nplan:\n%s", compositeIndexCols, plan)
	}
}

// literalizeParams replaces each ? placeholder in query, in order, with the
// corresponding literal — for EXPLAINing a production ?-bound SQL string whose
// planner shape is under test. It panics on an arity mismatch so a drifted
// placeholder count fails loudly rather than EXPLAINing malformed SQL.
func literalizeParams(query string, literals ...string) string {
	for _, lit := range literals {
		if !strings.Contains(query, "?") {
			panic("literalizeParams: more literals than ? placeholders in query")
		}
		query = strings.Replace(query, "?", lit, 1)
	}
	if strings.Contains(query, "?") {
		panic("literalizeParams: unbound ? placeholder(s) remain in query")
	}
	return query
}

// explainPlan runs EXPLAIN FORMAT=TREE <query> and joins the plan tree into one
// string. FORMAT=TREE yields the go-mysql-server node tree in a single "plan"
// column (IndexedTableAccess / Filter / TopN); the default tabular EXPLAIN is
// avoided because its numeric columns break the MySQL driver's row decode.
func explainPlan(t *testing.T, ctx context.Context, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN FORMAT=TREE "+query)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("EXPLAIN columns: %v", err)
	}
	var out strings.Builder
	for rows.Next() {
		// RawBytes bypasses the driver's per-column type coercion (Dolt's EXPLAIN
		// can carry numeric columns that arrive as the literal text "NULL").
		cells := make([]any, len(cols))
		holders := make([]sql.RawBytes, len(cols))
		for i := range cells {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("EXPLAIN scan: %v", err)
		}
		for _, h := range holders {
			if len(h) > 0 {
				out.Write(h)
				out.WriteString("\n")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}
	return out.String()
}

// looksLikeDoltPlan reports whether s resembles a go-mysql-server / Dolt EXPLAIN
// plan tree, so the sargability assertion can skip cleanly if the format shifts.
func looksLikeDoltPlan(s string) bool {
	for _, tok := range []string{"TableAccess", "Table", "Project", "Filter", "Sort", "Limit", "TopN"} {
		if strings.Contains(s, tok) {
			return true
		}
	}
	return false
}
