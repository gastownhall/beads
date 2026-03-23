//go:build embeddeddolt

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdComment runs "bd comments add" with the given args and returns stdout.
func bdComment(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"comments", "add"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd comments add %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdCommentList runs "bd comments list" and returns stdout.
func bdCommentList(t *testing.T, bd, dir, issueID string) string {
	t.Helper()
	cmd := exec.Command(bd, "comments", "list", issueID)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd comments list %s failed: %v\n%s", issueID, err, out)
	}
	return string(out)
}

func TestEmbeddedComments(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")

	t.Run("add_and_list_comment", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Comment target", "--type", "task")

		store := openStore(t, beadsDir, "cm")
		comment, err := store.AddIssueComment(t.Context(), issue.ID, "tester", "Hello world")
		if err != nil {
			t.Fatalf("AddIssueComment: %v", err)
		}
		if comment.Text != "Hello world" {
			t.Errorf("expected comment text 'Hello world', got %q", comment.Text)
		}
		if comment.Author != "tester" {
			t.Errorf("expected author 'tester', got %q", comment.Author)
		}
		if comment.ID == "" {
			t.Error("expected comment ID to be set")
		}

		// Verify via GetIssueComments.
		comments, err := store.GetIssueComments(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssueComments: %v", err)
		}
		found := false
		for _, c := range comments {
			if c.Text == "Hello world" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected to find 'Hello world' comment in GetIssueComments")
		}
	})

	t.Run("add_comment_event", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Event comment target", "--type", "task")

		store := openStore(t, beadsDir, "cm")
		if err := store.AddComment(t.Context(), issue.ID, "actor", "A comment event"); err != nil {
			t.Fatalf("AddComment: %v", err)
		}
	})

	t.Run("add_comment_nonexistent_issue", func(t *testing.T) {
		store := openStore(t, beadsDir, "cm")
		_, err := store.AddIssueComment(t.Context(), "cm-nonexistent999", "tester", "nope")
		if err == nil {
			t.Error("expected error for nonexistent issue")
		}
	})
}

func TestEmbeddedPromote(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pm")

	t.Run("promote_wisp", func(t *testing.T) {
		// Create an ephemeral issue (routes to wisps table).
		issue := bdCreate(t, bd, dir, "Promote me", "--ephemeral")

		store := openStore(t, beadsDir, "pm")

		// Verify it's a wisp before promote.
		got, err := store.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue before promote: %v", err)
		}
		if !got.Ephemeral {
			t.Skip("issue is not ephemeral; cannot test promote")
		}

		// Promote.
		if err := store.PromoteFromEphemeral(t.Context(), issue.ID, "tester"); err != nil {
			t.Fatalf("PromoteFromEphemeral: %v", err)
		}

		// Verify it's now permanent.
		got, err = store.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue after promote: %v", err)
		}
		if got.Ephemeral {
			t.Error("expected issue to be non-ephemeral after promote")
		}
	})

	t.Run("promote_nonexistent_wisp", func(t *testing.T) {
		store := openStore(t, beadsDir, "pm")
		err := store.PromoteFromEphemeral(t.Context(), "pm-nonexistent999", "tester")
		if err == nil {
			t.Error("expected error for nonexistent wisp")
		}
	})
}
