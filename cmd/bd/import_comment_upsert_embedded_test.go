//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bd import of a JSONL whose comment id already exists with edited text used to
// fail with "Error 1062: duplicate primary key".
func TestEmbeddedImportCommentUpsertByID(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imcu")

	const issueID = "imcu-aaa"
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	updated := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := time.Date(2026, 6, 1, 11, 30, 0, 0, time.UTC)

	writeRowAt := func(updatedAt time.Time, text string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, path, []types.Issue{{
			ID: issueID, Title: "Comment upsert", Status: types.StatusOpen, IssueType: types.TypeTask,
			CreatedAt: updated.Add(-time.Hour), UpdatedAt: updatedAt,
			Comments: []*types.Comment{{ID: commentID, IssueID: issueID, Author: "tester", Text: text, CreatedAt: commentAt}},
		}})
		return path
	}
	writeRow := func(text string) string { return writeRowAt(updated, text) }

	const skippedLine = "Skipped 1 comment edit(s) on issues whose local row is the same age or newer (use --allow-stale to overwrite)"
	wantSkipLine := func(stage, out string, want bool) {
		t.Helper()
		if got := strings.Contains(out, skippedLine); got != want {
			t.Fatalf("%s: skipped-edit line present = %v, want %v\noutput:\n%s", stage, got, want, out)
		}
		if !want && strings.Contains(out, "comment edit(s)") {
			t.Fatalf("%s: unexpected skipped-edit line\noutput:\n%s", stage, out)
		}
	}

	commentTexts := func() map[string]string {
		t.Helper()
		cmd := exec.Command(bd, "comments", issueID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd comments --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in bd comments output: %s", s)
		}
		var comments []types.Comment
		if err := json.Unmarshal([]byte(s[start:]), &comments); err != nil {
			t.Fatalf("parse comments JSON: %v\n%s", err, s)
		}
		texts := make(map[string]string, len(comments))
		for _, c := range comments {
			texts[c.ID] = c.Text
		}
		return texts
	}

	want := func(stage, text string) {
		t.Helper()
		texts := commentTexts()
		if len(texts) != 1 || texts[commentID] != text {
			t.Fatalf("%s: comments = %v, want exactly {%s: %q}", stage, texts, commentID, text)
		}
	}

	out := bdImport(t, bd, dir, writeRow("original"))
	want("initial import", "original")
	wantSkipLine("initial import", out, false)

	out = bdImport(t, bd, dir, writeRow("original"))
	want("identical re-import", "original")
	wantSkipLine("identical re-import", out, false)

	out = bdImport(t, bd, dir, writeRow("edited"))
	want("edited comment, equal updated_at", "original")
	wantSkipLine("edited comment, equal updated_at", out, true)

	out = bdImport(t, bd, dir, "--allow-stale", writeRow("edited"))
	want("edited comment, --allow-stale", "edited")
	wantSkipLine("edited comment, --allow-stale", out, false)

	bdImport(t, bd, dir, "--allow-stale", writeRow("edited"))
	want("--allow-stale re-import", "edited")

	out = bdImport(t, bd, dir, writeRowAt(updated.Add(-time.Hour), "older edit"))
	want("edited comment, stale row", "edited")
	wantSkipLine("edited comment, stale row", out, true)

	// Sub-second updated_at: the report must agree with the write, which
	// rounds into the DATETIME(0) column (+300ms ties, +700ms is newer).
	out = bdImport(t, bd, dir, writeRowAt(updated.Add(300*time.Millisecond), "sub-second edit"))
	want("edited comment, +300ms", "edited")
	wantSkipLine("edited comment, +300ms", out, true)

	out = bdImport(t, bd, dir, writeRowAt(updated.Add(700*time.Millisecond), "sub-second edit"))
	want("edited comment, +700ms", "sub-second edit")
	wantSkipLine("edited comment, +700ms", out, false)
}
