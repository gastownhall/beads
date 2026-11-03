//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// bd-413ovu: bd import of a snapshot whose comment id is already stored with
// edited content keeps the stored comment instead of failing on the comments
// primary key (Error 1062). A default re-import of that equal-timestamp
// snapshot is provably unchanged, so it writes no journal, version or Dolt
// history for the issue, however many times it runs.
func TestEmbeddedImportCommentIDMatch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "imcid")
	bdRunOK(t, bd, dir, "config", "set", "events-journal", "true")

	const issueID = "imcid-aaa"
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	snapshot := func(text string) string {
		path := filepath.Join(t.TempDir(), "issues.jsonl")
		writeJSONLFile(t, path, []types.Issue{{
			ID: issueID, Title: "comment id match", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, CreatedAt: at, UpdatedAt: at,
			Comments: []*types.Comment{{ID: commentID, IssueID: issueID, Author: "alice", Text: text, CreatedAt: at}},
		}})
		return path
	}

	type counts struct{ journal, commentJournal, versions, commits int }
	read := func() (counts, string) {
		t.Helper()
		database := ""
		if cfg, _ := configfile.Load(beadsDir); cfg != nil {
			database = cfg.GetDoltDatabase()
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var c counts
		var text string
		for _, q := range []struct {
			query string
			args  []any
			dest  any
		}{
			{"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ?", []any{issueID}, &c.journal},
			{"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'", []any{issueID}, &c.commentJournal},
			{"SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{issueID}, &c.versions},
			{"SELECT COUNT(*) FROM dolt_log", nil, &c.commits},
			{"SELECT text FROM comments WHERE id = ?", []any{commentID}, &text},
		} {
			if err := db.QueryRowContext(t.Context(), q.query, q.args...).Scan(q.dest); err != nil {
				t.Fatalf("%s: %v", q.query, err)
			}
		}
		return c, text
	}

	bdImport(t, bd, dir, snapshot("original text"))
	created, text := read()
	if text != "original text" {
		t.Fatalf("comment text after first import = %q, want %q", text, "original text")
	}
	if created.journal == 0 || created.commentJournal != 1 {
		t.Fatalf("first import journaled %d rows (%d comment), want the create and one comment write", created.journal, created.commentJournal)
	}

	edited := snapshot("edited text")
	for run := 1; run <= 2; run++ {
		out := bdImport(t, bd, dir, edited)
		if !strings.Contains(out, "1 already present, unchanged") {
			t.Fatalf("default re-import %d: output %q does not report the issue unchanged", run, out)
		}
		got, text := read()
		if text != "original text" {
			t.Fatalf("default re-import %d: comment text = %q, want the stored %q", run, text, "original text")
		}
		if got != created {
			t.Fatalf("default re-import %d wrote history: counts %+v, want unchanged %+v", run, got, created)
		}
	}

	bdImport(t, bd, dir, "--allow-stale", edited)
	got, text := read()
	if text != "original text" {
		t.Fatalf("--allow-stale import: comment text = %q, want the stored %q", text, "original text")
	}
	if got.commentJournal != created.commentJournal {
		t.Fatalf("--allow-stale import journaled %d comment writes, want none", got.commentJournal-created.commentJournal)
	}
}
