//go:build cgo

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedImportCommentIDMatch runs `bd import` end to end over a comment
// whose id is already stored on the same issue (bd-413ovu, PR #7114): the
// import keeps the stored comment text, and an equal-timestamp snapshot whose
// only difference is the edited comment is classified unchanged, so repeat
// imports write no journal or version rows for the issue. `--allow-stale`
// rewrites the issue row (its own reporting) and still keeps the comment.
func TestEmbeddedImportCommentIDMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("embedded dolt import test")
	}
	if env := os.Getenv("BEADS_TEST_EMBEDDED_DOLT"); env != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "icu")
	ctx := t.Context()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := base.Add(-time.Hour)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	issueRow := func(commentText string, updatedAt time.Time) types.Issue {
		return types.Issue{
			ID: "icu-aaa", Title: "comment id import", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: updatedAt,
			Comments: []*types.Comment{{
				ID: commentID, IssueID: "icu-aaa", Author: "alice",
				Text: commentText, CreatedAt: commentAt,
			}},
		}
	}

	importSnapshot := func(name string, row types.Issue) string {
		t.Helper()
		jsonlPath := filepath.Join(t.TempDir(), name)
		writeJSONLFile(t, jsonlPath, []types.Issue{row})
		return bdImport(t, bd, dir, jsonlPath)
	}

	// withDB runs fn on a raw SQL connection to the workspace's embedded
	// database. The connection is opened and closed around the bd subprocess
	// runs only: the embedded engine serializes writers, so holding it while
	// a bd subprocess opens the same database would deadlock both.
	withDB := func(fn func(db *sql.DB)) {
		t.Helper()
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() {
			if err := cleanup(); err != nil {
				t.Fatalf("close raw SQL connection: %v", err)
			}
		}()
		fn(db)
	}

	scalar := func(db *sql.DB, query string, args ...any) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return count
	}
	storedCommentText := func(db *sql.DB) string {
		t.Helper()
		var text string
		if err := db.QueryRowContext(ctx, "SELECT text FROM comments WHERE id = ?", commentID).Scan(&text); err != nil {
			t.Fatalf("read stored comment: %v", err)
		}
		return text
	}
	type issueWriteCounts struct {
		journal, commentOps, versions int
	}
	writeCounts := func() issueWriteCounts {
		t.Helper()
		var got issueWriteCounts
		withDB(func(db *sql.DB) {
			got = issueWriteCounts{
				journal:    scalar(db, "SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ?", "icu-aaa"),
				commentOps: scalar(db, "SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'", "icu-aaa"),
				versions:   scalar(db, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", "icu-aaa"),
			}
		})
		return got
	}

	// Initial import: the comment is inserted and journaled once.
	out := importSnapshot("initial.jsonl", issueRow("original text", base))
	if !strings.Contains(out, "Imported 1 issue") {
		t.Fatalf("initial import output = %q, want a created issue", out)
	}
	var afterInsert issueWriteCounts
	withDB(func(db *sql.DB) {
		if got := storedCommentText(db); got != "original text" {
			t.Fatalf("stored comment text = %q, want %q", got, "original text")
		}
	})
	afterInsert = writeCounts()

	// Default tie import with an edited comment: the row is provably
	// unchanged (the id is stored on the issue), so the run writes no
	// journal or version rows, and the comment keeps its stored text.
	out = importSnapshot("tie-edited.jsonl", issueRow("edited text", base))
	if !strings.Contains(out, "1 already present, unchanged") {
		t.Fatalf("tie import output = %q, want the row reported as already present, unchanged", out)
	}
	withDB(func(db *sql.DB) {
		if got := storedCommentText(db); got != "original text" {
			t.Fatalf("stored comment text after tie import = %q, want the stored %q kept", got, "original text")
		}
	})
	if got := writeCounts(); got != afterInsert {
		t.Fatalf("writes after tie import = %+v, want %+v (no journal or version rows)", got, afterInsert)
	}

	// Re-running the same tie import converges: still nothing written.
	out = importSnapshot("tie-edited-repeat.jsonl", issueRow("edited text", base))
	if !strings.Contains(out, "1 already present, unchanged") {
		t.Fatalf("repeat tie import output = %q, want the row reported as already present, unchanged", out)
	}
	withDB(func(db *sql.DB) {
		if got := storedCommentText(db); got != "original text" {
			t.Fatalf("stored comment text after repeat tie import = %q, want the stored %q kept", got, "original text")
		}
	})
	if got := writeCounts(); got != afterInsert {
		t.Fatalf("writes after repeat tie import = %+v, want %+v (no journal or version rows)", got, afterInsert)
	}

	// --allow-stale rewrites the issue row (its own reporting) and still
	// keeps the stored comment: no comment write, no duplicate row.
	out = importSnapshot("allow-stale.jsonl", issueRow("stale edited text", base.Add(time.Hour)))
	if !strings.Contains(out, "Imported 1 issue") {
		t.Fatalf("--allow-stale import output = %q, want the row imported", out)
	}
	var afterAllowStale issueWriteCounts
	withDB(func(db *sql.DB) {
		if got := storedCommentText(db); got != "original text" {
			t.Fatalf("stored comment text after --allow-stale import = %q, want the stored %q kept", got, "original text")
		}
		if got := scalar(db, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", "icu-aaa"); got != 1 {
			t.Fatalf("comment count on icu-aaa = %d, want 1 (no duplicate)", got)
		}
	})
	afterAllowStale = writeCounts()
	if afterAllowStale.commentOps != afterInsert.commentOps {
		t.Fatalf("comment journal ops after --allow-stale = %d, want %d (no comment write on the id match)",
			afterAllowStale.commentOps, afterInsert.commentOps)
	}
}