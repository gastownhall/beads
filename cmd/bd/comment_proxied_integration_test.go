//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func bdProxiedComment(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, append([]string{"comment"}, args...)...)
	if err != nil {
		t.Fatalf("bd comment %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func TestProxiedServerComment(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("permanent_comment_round_trip_and_not_found", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cm")
		issue := bdProxiedCreate(t, bd, p.dir, "Needs a comment")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comment", issue.ID, "first thoughts")
		if err != nil {
			t.Fatalf("bd comment failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comment stderr = %q, want empty", stderr)
		}
		if !strings.Contains(stdout, "Comment added to "+issue.ID) {
			t.Errorf("bd comment confirmation = %q, want exact issue ID %q", stdout, issue.ID)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", "--json", issue.ID)
		if err != nil {
			t.Fatalf("bd comments --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments --json stderr = %q, want empty", stderr)
		}
		var comments []types.Comment
		if err := json.Unmarshal([]byte(stdout), &comments); err != nil {
			t.Fatalf("decode comments JSON: %v\nraw: %q", err, stdout)
		}
		if len(comments) != 1 {
			t.Fatalf("comments length = %d, want 1", len(comments))
		}
		comment := comments[0]
		if comment.IssueID != issue.ID {
			t.Errorf("comment issue ID = %q, want %q", comment.IssueID, issue.ID)
		}
		if comment.Author != "Test" {
			t.Errorf("comment author = %q, want %q", comment.Author, "Test")
		}
		if comment.Text != "first thoughts" {
			t.Errorf("comment text = %q, want %q", comment.Text, "first thoughts")
		}
		if comment.ID == "" {
			t.Error("comment ID is empty")
		}
		if comment.CreatedAt.IsZero() || comment.CreatedAt.Nanosecond() != 0 {
			t.Errorf("comment created_at = %v, want nonzero whole-second timestamp", comment.CreatedAt)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comment", "--json", issue.ID, "json body")
		if err != nil {
			t.Fatalf("bd comment --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comment --json stderr = %q, want empty", stderr)
		}
		var jsonComment types.Comment
		if err := json.Unmarshal([]byte(stdout), &jsonComment); err != nil {
			t.Fatalf("decode comment JSON: %v\nraw: %q", err, stdout)
		}
		if jsonComment.IssueID != issue.ID {
			t.Errorf("JSON comment issue ID = %q, want %q", jsonComment.IssueID, issue.ID)
		}
		if jsonComment.Text != "json body" {
			t.Errorf("JSON comment text = %q, want %q", jsonComment.Text, "json body")
		}
		if jsonComment.Author != "Test" {
			t.Errorf("JSON comment author = %q, want %q", jsonComment.Author, "Test")
		}
		if jsonComment.ID == "" {
			t.Error("JSON comment ID is empty")
		}
		if jsonComment.CreatedAt.IsZero() || jsonComment.CreatedAt.Nanosecond() != 0 {
			t.Errorf("JSON comment created_at = %v, want nonzero whole-second timestamp", jsonComment.CreatedAt)
		}

		fileDir := t.TempDir()
		commentFile := filepath.Join(fileDir, "comment.txt")
		if err := os.WriteFile(commentFile, []byte("body from a file"), 0o600); err != nil {
			t.Fatalf("write comment file: %v", err)
		}
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comment", issue.ID, "--file", commentFile)
		if err != nil {
			t.Fatalf("bd comment --file failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comment --file stderr = %q, want empty", stderr)
		}

		addFile := filepath.Join(fileDir, "add.txt")
		if err := os.WriteFile(addFile, []byte("note from file"), 0o600); err != nil {
			t.Fatalf("write comments add file: %v", err)
		}
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", issue.ID, "--file", addFile)
		if err != nil {
			t.Fatalf("bd comments add --file failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments add --file stderr = %q, want empty", stderr)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comment", issue.ID, "   ")
		if err == nil {
			t.Fatalf("bd comment with whitespace text unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if stdout != "" {
			t.Errorf("whitespace-comment stdout = %q, want empty", stdout)
		}
		if got, want := strings.TrimSpace(stderr), "Error: comment text cannot be empty"; got != want {
			t.Errorf("whitespace-comment stderr = %q, want %q", got, want)
		}

		uncommented := bdProxiedCreate(t, bd, p.dir, "Uncommented")
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", uncommented.ID)
		if err != nil {
			t.Fatalf("bd comments on uncommented issue failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments on uncommented issue stderr = %q, want empty", stderr)
		}
		if got, want := stdout, "No comments on "+uncommented.ID+"\n"; got != want {
			t.Errorf("empty comments output = %q, want %q", got, want)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", "--local-time", issue.ID)
		if err != nil {
			t.Fatalf("bd comments --local-time failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments --local-time stderr = %q, want empty", stderr)
		}
		if stdout == "" || !strings.Contains(stdout, "first thoughts") {
			t.Errorf("bd comments --local-time output = %q, want rendered comment text", stdout)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", "--json", issue.ID)
		if err != nil {
			t.Fatalf("final bd comments --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("final bd comments --json stderr = %q, want empty", stderr)
		}
		if err := json.Unmarshal([]byte(stdout), &comments); err != nil {
			t.Fatalf("decode final comments JSON: %v\nraw: %q", err, stdout)
		}
		if len(comments) != 4 {
			t.Fatalf("final comments length = %d, want 4", len(comments))
		}
		expectedTexts := map[string]struct{}{
			"first thoughts":   {},
			"json body":        {},
			"body from a file": {},
			"note from file":   {},
		}
		for _, listed := range comments {
			if _, ok := expectedTexts[listed.Text]; !ok {
				t.Errorf("unexpected final comment text %q", listed.Text)
				continue
			}
			delete(expectedTexts, listed.Text)
			if listed.IssueID != issue.ID {
				t.Errorf("final comment %q issue ID = %q, want %q", listed.Text, listed.IssueID, issue.ID)
			}
			if listed.Author != "Test" {
				t.Errorf("final comment %q author = %q, want %q", listed.Text, listed.Author, "Test")
			}
			if listed.ID == "" {
				t.Errorf("final comment %q has empty ID", listed.Text)
			}
			if listed.CreatedAt.IsZero() || listed.CreatedAt.Nanosecond() != 0 {
				t.Errorf("final comment %q created_at = %v, want nonzero whole-second timestamp", listed.Text, listed.CreatedAt)
			}
		}
		if len(expectedTexts) != 0 {
			t.Errorf("missing final comment texts: %v", expectedTexts)
		}

		missingID := "cm-99999"
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comment", missingID, "orphan comment")
		if err == nil {
			t.Fatalf("bd comment %s unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", missingID, stdout, stderr)
		}
		if stdout != "" {
			t.Errorf("missing-ID stdout = %q, want empty", stdout)
		}
		if got, want := strings.TrimSpace(stderr), "Error: issue "+missingID+" not found"; got != want {
			t.Errorf("missing-ID stderr = %q, want %q", got, want)
		}
	})

	t.Run("wisp_comments_add_round_trip_and_physical_routing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wa")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp target", "--ephemeral")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", wisp.ID, "wisp", "via", "add", "--author", "contract-author", "--json")
		if err != nil {
			t.Fatalf("bd comments add --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments add --json stderr = %q, want empty", stderr)
		}
		var added types.Comment
		if err := json.Unmarshal([]byte(stdout), &added); err != nil {
			t.Fatalf("decode added comment JSON: %v\nraw: %q", err, stdout)
		}
		if added.IssueID != wisp.ID {
			t.Errorf("added comment issue ID = %q, want %q", added.IssueID, wisp.ID)
		}
		if added.Author != "contract-author" {
			t.Errorf("added comment author = %q, want %q", added.Author, "contract-author")
		}
		if added.Text != "wisp via add" {
			t.Errorf("added comment text = %q, want %q", added.Text, "wisp via add")
		}
		if added.ID == "" {
			t.Error("added comment ID is empty")
		}
		if added.CreatedAt.IsZero() || added.CreatedAt.Nanosecond() != 0 {
			t.Errorf("added comment created_at = %v, want nonzero whole-second timestamp", added.CreatedAt)
		}

		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "comments", "--json", wisp.ID)
		if err != nil {
			t.Fatalf("bd comments --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if stderr != "" {
			t.Errorf("bd comments --json stderr = %q, want empty", stderr)
		}
		var comments []types.Comment
		if err := json.Unmarshal([]byte(stdout), &comments); err != nil {
			t.Fatalf("decode wisp comments JSON: %v\nraw: %q", err, stdout)
		}
		if len(comments) != 1 {
			t.Fatalf("wisp comments length = %d, want 1", len(comments))
		}
		if comments[0] != added {
			t.Errorf("listed comment = %+v, want %+v", comments[0], added)
		}

		db := openProxiedDB(t, p)
		var wispCount, permanentCount int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", wisp.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_comments: %v", err)
		}
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM comments WHERE issue_id = ?", wisp.ID).Scan(&permanentCount); err != nil {
			t.Fatalf("count comments: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_comments count = %d, want 1", wispCount)
		}
		if permanentCount != 0 {
			t.Errorf("comments count = %d, want 0", permanentCount)
		}
	})
}
