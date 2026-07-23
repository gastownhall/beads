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
	fullArgs := append([]string{"comment"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd comment %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedCommentFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"comment"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd comment %s to fail, got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedComments(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"comments"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd comments %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedCommentsJSON(t *testing.T, bd, dir string, args ...string) []*types.Comment {
	t.Helper()
	fullArgs := append([]string{"comments", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd comments --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in comments output:\n%s", stdout)
	}
	var comments []*types.Comment
	if err := json.Unmarshal([]byte(stdout[start:]), &comments); err != nil {
		t.Fatalf("parse comments JSON: %v\nraw: %s", err, stdout[start:])
	}
	return comments
}

func TestProxiedServerComment(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("comment_shorthand_then_list", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cm")
		issue := bdProxiedCreate(t, bd, p.dir, "Needs a comment")

		out := bdProxiedComment(t, bd, p.dir, issue.ID, "first thoughts")
		if !strings.Contains(out, "Comment added to") {
			t.Errorf("expected confirmation, got:\n%s", out)
		}

		listed := bdProxiedComments(t, bd, p.dir, issue.ID)
		if !strings.Contains(listed, "first thoughts") {
			t.Errorf("expected listed comment text, got:\n%s", listed)
		}
	})

	t.Run("comments_add_subcommand", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ca")
		issue := bdProxiedCreate(t, bd, p.dir, "Add via subcommand")

		if _, _, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", issue.ID, "via add"); err != nil {
			t.Fatalf("comments add failed: %v", err)
		}

		comments := bdProxiedCommentsJSON(t, bd, p.dir, issue.ID)
		if len(comments) != 1 {
			t.Fatalf("expected 1 comment, got %d", len(comments))
		}
		if comments[0].Text != "via add" {
			t.Errorf("text: got %q, want %q", comments[0].Text, "via add")
		}
	})

	t.Run("multiple_comments_ordered", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mo")
		issue := bdProxiedCreate(t, bd, p.dir, "Multi comment")

		bdProxiedComment(t, bd, p.dir, issue.ID, "one")
		bdProxiedComment(t, bd, p.dir, issue.ID, "two")
		bdProxiedComment(t, bd, p.dir, issue.ID, "three")

		comments := bdProxiedCommentsJSON(t, bd, p.dir, issue.ID)
		if len(comments) != 3 {
			t.Fatalf("expected 3 comments, got %d", len(comments))
		}
		if comments[0].Text != "one" || comments[1].Text != "two" || comments[2].Text != "three" {
			t.Errorf("unexpected ordering: %q, %q, %q", comments[0].Text, comments[1].Text, comments[2].Text)
		}
	})

	t.Run("comment_json_output", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cj")
		issue := bdProxiedCreate(t, bd, p.dir, "JSON comment")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comment", "--json", issue.ID, "json body")
		if err != nil {
			t.Fatalf("comment --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON object in output:\n%s", stdout)
		}
		var c types.Comment
		if err := json.Unmarshal([]byte(stdout[start:]), &c); err != nil {
			t.Fatalf("parse comment JSON: %v\nraw: %s", err, stdout[start:])
		}
		if c.Text != "json body" {
			t.Errorf("text: got %q, want %q", c.Text, "json body")
		}
		if c.IssueID != issue.ID {
			t.Errorf("issue_id: got %q, want %q", c.IssueID, issue.ID)
		}
		if c.ID == "" {
			t.Error("expected comment ID")
		}
	})

	t.Run("no_comments_message", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "nc")
		issue := bdProxiedCreate(t, bd, p.dir, "Uncommented")

		out := bdProxiedComments(t, bd, p.dir, issue.ID)
		if !strings.Contains(out, "No comments on") {
			t.Errorf("expected empty-state message, got:\n%s", out)
		}
	})

	t.Run("empty_text_fails", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "et")
		issue := bdProxiedCreate(t, bd, p.dir, "Empty text")

		out := bdProxiedCommentFail(t, bd, p.dir, issue.ID, "")
		if !strings.Contains(out, "empty") {
			t.Errorf("expected empty-text error, got:\n%s", out)
		}
	})

	t.Run("missing_issue_fails", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mi")
		out := bdProxiedCommentFail(t, bd, p.dir, "mi-99999", "orphan comment")
		if !strings.Contains(out, "not found") {
			t.Errorf("expected not-found error, got:\n%s", out)
		}
	})

	t.Run("comments_add_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "aj")
		issue := bdProxiedCreate(t, bd, p.dir, "Add JSON")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", issue.ID, "add json body", "--json")
		if err != nil {
			t.Fatalf("comments add --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON object in output:\n%s", stdout)
		}
		var c types.Comment
		if err := json.Unmarshal([]byte(stdout[start:]), &c); err != nil {
			t.Fatalf("parse comment JSON: %v\nraw: %s", err, stdout[start:])
		}
		if c.Text != "add json body" {
			t.Errorf("text: got %q, want %q", c.Text, "add json body")
		}
		if c.IssueID != issue.ID {
			t.Errorf("issue_id: got %q, want %q", c.IssueID, issue.ID)
		}
	})

	t.Run("comment_from_file", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cf")
		issue := bdProxiedCreate(t, bd, p.dir, "Comment from file")

		file := filepath.Join(t.TempDir(), "body.txt")
		if err := os.WriteFile(file, []byte("body from a file"), 0o644); err != nil {
			t.Fatalf("write comment file: %v", err)
		}

		out := bdProxiedComment(t, bd, p.dir, issue.ID, "--file", file)
		if !strings.Contains(out, "Comment added to") {
			t.Errorf("expected confirmation, got:\n%s", out)
		}

		comments := bdProxiedCommentsJSON(t, bd, p.dir, issue.ID)
		if len(comments) != 1 || comments[0].Text != "body from a file" {
			t.Fatalf("expected 1 file-sourced comment, got %+v", comments)
		}
	})

	t.Run("comments_add_from_file", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "af")
		issue := bdProxiedCreate(t, bd, p.dir, "Add from file")

		file := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(file, []byte("note from file"), 0o644); err != nil {
			t.Fatalf("write comment file: %v", err)
		}

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", issue.ID, "-f", file); err != nil {
			t.Fatalf("comments add -f failed: %v\nstderr:\n%s", err, stderr)
		}

		comments := bdProxiedCommentsJSON(t, bd, p.dir, issue.ID)
		if len(comments) != 1 || comments[0].Text != "note from file" {
			t.Fatalf("expected 1 file-sourced comment, got %+v", comments)
		}
	})

	t.Run("comments_list_local_time", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lt")
		issue := bdProxiedCreate(t, bd, p.dir, "Local time")
		bdProxiedComment(t, bd, p.dir, issue.ID, "timed comment")

		out := bdProxiedComments(t, bd, p.dir, issue.ID, "--local-time")
		if !strings.Contains(out, "timed comment") {
			t.Errorf("expected comment text with --local-time, got:\n%s", out)
		}
	})

	t.Run("comment_on_wisp_routes_to_wisp_comments", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wc")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp target", "--ephemeral")

		out := bdProxiedComment(t, bd, p.dir, wisp.ID, "wisp comment")
		if !strings.Contains(out, "Comment added to") {
			t.Errorf("expected confirmation, got:\n%s", out)
		}

		comments := bdProxiedCommentsJSON(t, bd, p.dir, wisp.ID)
		if len(comments) != 1 || comments[0].Text != "wisp comment" {
			t.Fatalf("expected 1 wisp comment via list, got %+v", comments)
		}

		db := openProxiedDB(t, p)
		var wispCount, permCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", wisp.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_comments: %v", err)
		}
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM comments WHERE issue_id = ?", wisp.ID).Scan(&permCount); err != nil {
			t.Fatalf("count comments: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_comments count = %d, want 1", wispCount)
		}
		if permCount != 0 {
			t.Errorf("comments (permanent) count = %d, want 0 — wisp comment must not leak into the permanent table", permCount)
		}
	})

	t.Run("comments_add_on_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wa")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp add target", "--ephemeral")

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comments", "add", wisp.ID, "wisp via add"); err != nil {
			t.Fatalf("comments add on wisp failed: %v\nstderr:\n%s", err, stderr)
		}

		comments := bdProxiedCommentsJSON(t, bd, p.dir, wisp.ID)
		if len(comments) != 1 || comments[0].Text != "wisp via add" {
			t.Fatalf("expected 1 wisp comment, got %+v", comments)
		}
	})
}
