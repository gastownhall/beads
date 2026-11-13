//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdShowRaw runs "bd show" with the given args and returns raw stdout.
// Retries on flock contention.
func bdShowRaw(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd show %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdShowDetails runs "bd show <id> --json [extra...]" and parses the
// IssueDetails. Retries on flock contention.
func bdShowDetails(t *testing.T, bd, dir, id string, extra ...string) map[string]interface{} {
	t.Helper()
	args := append([]string{"show", id, "--json"}, extra...)
	out, err := bdRunWithFlockRetry(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	// JSON output may be wrapped in an array
	s := strings.TrimSpace(string(out))
	// Find JSON start
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in show output: %s", s)
	}
	s = s[start:]

	// If array, unwrap first element
	if strings.HasPrefix(s, "[") {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			t.Fatalf("parse show JSON array: %v\n%s", err, s)
		}
		if len(arr) == 0 {
			t.Fatal("empty JSON array from show")
		}
		return arr[0]
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, s)
	}
	return m
}

// assertLiveRevisionToken holds a `revision` read off a `bd show --json` detail
// view to the wire contract the token carries: a decimal STRING, never a JSON
// number.
//
// THE TYPE IS ASSERTED BEFORE THE VALUE, and that ordering is the point. The
// token spans the full int64 range, and a JSON number past 2^53 is rounded by
// every double-based consumer — jq, JavaScript, Go's own `any` — so a guard
// composed from one is refused against a row nothing else touched. Decoding a
// detail view into `map[string]any` is exactly the shape that hides it, which is
// why both callers here go through this helper instead of a local cast.
//
// "LIVE" IS THE SECOND HALF. A row the caller just created carries a freshly
// minted token, so "0" — the migration-0054 backfill value — means the producer
// published a member it never filled in: the one failure a legacy-zero row makes
// otherwise indistinguishable from success.
func assertLiveRevisionToken(t *testing.T, value any) {
	t.Helper()
	token, ok := value.(string)
	if !ok {
		t.Errorf("revision = %#v (%T), want the decimal string the wire contract declares", value, value)
		return
	}
	parsed, err := types.ParseRevisionToken(token)
	if err != nil {
		t.Errorf("revision = %q, which is not a decimal token: %v", token, err)
		return
	}
	if got := types.RevisionToken(parsed); got != token {
		t.Errorf("revision = %q does not round-trip through the token codec (got %q back)", token, got)
	}
	if parsed == 0 {
		t.Errorf("revision = %q on a row this test just created; the token was not read off the row", token)
	}
}

// bdShowFail2 runs "bd show" expecting failure.
func bdShowFail2(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd show %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// TestEmbeddedShowBasicsAndJSON was split from TestEmbeddedShow (originally
// ~193s, measured under --config=embedded) into 2 top-level tests over
// disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once. show_current_fallback_to_last_touched
// (in the second group) only requires that SOME earlier subtest in its own
// group already created/touched an issue in the shared dir — it is placed
// after several issue-creating subtests in that group, so the redone,
// self-contained setup still satisfies it.
func TestEmbeddedShowBasicsAndJSON(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ts")

	// ===== Basic Show =====

	t.Run("show_single_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Show me", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Show me") {
			t.Errorf("expected title in output: %s", out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in output: %s", out)
		}
	})

	t.Run("show_multiple_issues", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi 2", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue1.ID, issue2.ID)
		if !strings.Contains(out, "Multi 1") {
			t.Errorf("expected first issue title: %s", out)
		}
		if !strings.Contains(out, "Multi 2") {
			t.Errorf("expected second issue title: %s", out)
		}
	})

	t.Run("show_nonexistent_id", func(t *testing.T) {
		bdShowFail2(t, bd, dir, "ts-nonexistent999")
	})

	// A watch on an id that does not exist used to print "Issue not found"
	// and exit 0 on this route, watching nothing. It now exits 1 like plain
	// `bd show <missing>`, and like the proxied route.
	t.Run("show_watch_nonexistent_id_exits_1", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bd, "show", "ts-nonexistent999", "--watch")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("bd show --watch on a missing id kept watching:\n%s", out)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("bd show --watch on a missing id: err=%v, want exit 1\n%s", err, out)
		}
		if !strings.Contains(string(out), "ts-nonexistent999") {
			t.Errorf("output does not name the missing id:\n%s", out)
		}
	})

	t.Run("show_no_args", func(t *testing.T) {
		bdShowFail2(t, bd, dir)
	})

	// ===== --json =====

	t.Run("show_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON show", "--type", "task", "--description", "A description")
		m := bdShowDetails(t, bd, dir, issue.ID)
		if m["id"] != issue.ID {
			t.Errorf("expected id=%s, got %v", issue.ID, m["id"])
		}
		if m["title"] != "JSON show" {
			t.Errorf("expected title='JSON show', got %v", m["title"])
		}
		if m["description"] != "A description" {
			t.Errorf("expected description, got %v", m["description"])
		}
		assertLiveRevisionToken(t, m["revision"])
	})

	t.Run("show_json_includes_labels", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Labeled show", "--type", "task", "--label", "bug")
		m := bdShowDetails(t, bd, dir, issue.ID)
		labels, ok := m["labels"].([]interface{})
		if !ok {
			t.Fatalf("expected labels array, got %T", m["labels"])
		}
		found := false
		for _, l := range labels {
			if l == "bug" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'bug' in labels: %v", labels)
		}
	})

	t.Run("show_json_includes_dependencies", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Dep parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Dep child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		m := bdShowDetails(t, bd, dir, child.ID)
		deps, _ := m["dependencies"].([]interface{})
		if len(deps) == 0 {
			t.Error("expected dependencies in JSON output")
		}
	})

	t.Run("show_json_includes_comments", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Commented show", "--type", "task")
		store := openStore(t, beadsDir, "ts")
		_, _ = store.AddIssueComment(t.Context(), issue.ID, "tester", "A comment")
		store.Close() // release flock before subprocess

		// Comments are count-only by default; --include-comments streams them.
		out, err := bdRunWithFlockRetry(t, bd, dir, "show", issue.ID, "--json", "--include-comments")
		if err != nil {
			t.Fatalf("bd show --include-comments failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		if start := strings.IndexAny(s, "[{"); start >= 0 {
			s = s[start:]
		}
		var m map[string]interface{}
		if strings.HasPrefix(s, "[") {
			var arr []map[string]interface{}
			if jerr := json.Unmarshal([]byte(s), &arr); jerr != nil || len(arr) == 0 {
				t.Fatalf("parse show JSON array: %v\n%s", jerr, s)
			}
			m = arr[0]
		} else if jerr := json.Unmarshal([]byte(s), &m); jerr != nil {
			t.Fatalf("parse show JSON: %v\n%s", jerr, s)
		}
		comments, _ := m["comments"].([]interface{})
		if len(comments) == 0 {
			t.Error("expected comments in JSON output with --include-comments")
		}
	})

	// GH#5565: the direct/embedded twin of the proxied
	// show_wisp_comments_default_count_only. A wisp's comments live in
	// wisp_comments; the default count-only view must count them there.
	t.Run("show_json_wisp_comment_count", func(t *testing.T) {
		wisp := bdCreate(t, bd, dir, "Wisp w/comments", "--type", "task", "--ephemeral")
		for i := 0; i < 2; i++ {
			if out, err := bdRunWithFlockRetry(t, bd, dir, "comments", "add", wisp.ID, fmt.Sprintf("wisp comment %d", i)); err != nil {
				t.Fatalf("bd comments add failed: %v\n%s", err, out)
			}
		}

		m := bdShowDetails(t, bd, dir, wisp.ID)
		if got, _ := m["comment_count"].(float64); got != 2 {
			t.Errorf("comment_count: got %v, want 2", m["comment_count"])
		}
		if _, ok := m["comments"]; ok {
			t.Errorf("comments slice should be absent by default")
		}
		if omitted, _ := m["comments_omitted"].(bool); !omitted {
			t.Errorf("comments_omitted: got %v, want true beside a nonzero comment_count and no rows", m["comments_omitted"])
		}
	})

	// GH#5565 / bd-u96si: direct/embedded twin of proxied
	// show_wisp_comments_include_streams. --include-comments must stream the
	// wisp's comments from wisp_comments, the count in the same payload must
	// agree, and comments_omitted must stay omitted.
	t.Run("show_json_wisp_comments_include_streams", func(t *testing.T) {
		wisp := bdCreate(t, bd, dir, "Wisp stream", "--type", "task", "--ephemeral")
		for i := 0; i < 2; i++ {
			if out, err := bdRunWithFlockRetry(t, bd, dir, "comments", "add", wisp.ID, fmt.Sprintf("wisp stream %d", i)); err != nil {
				t.Fatalf("bd comments add failed: %v\n%s", err, out)
			}
		}

		m := bdShowDetails(t, bd, dir, wisp.ID, "--include-comments")
		comments, _ := m["comments"].([]interface{})
		if len(comments) != 2 {
			t.Fatalf("comments: got %d rows, want 2 with --include-comments: %v", len(comments), m)
		}
		if got, _ := m["comment_count"].(float64); got != 2 {
			t.Errorf("comment_count: got %v, want 2 beside the 2 streamed rows", m["comment_count"])
		}
		if v, present := m["comments_omitted"]; present {
			t.Errorf("comments_omitted should be absent under --include-comments, got %v", v)
		}
	})

	// ===== --short =====

	t.Run("show_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Short show", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue.ID, "--short")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		// Short mode should be compact — one line per issue (maybe with ANSI codes)
		if len(lines) > 3 {
			t.Errorf("expected compact output, got %d lines:\n%s", len(lines), out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in short output: %s", out)
		}
	})
}

// TestEmbeddedShowDetailAndCurrentFlags and
// TestEmbeddedShowDetailAndCurrentHistoryAndCurrent were split from
// TestEmbeddedShowDetailAndCurrent into two top-level tests over disjoint
// subtest groups, each on its own workspace, for CI shard balance. Every
// subtest is preserved exactly once, in its original order within its group;
// show_current_fallback_to_last_touched still follows subtests that show
// issues (show_as_of, show_local_time).
func TestEmbeddedShowDetailAndCurrentFlags(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ts")

	// ===== --long =====

	t.Run("show_long", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Long show", "--type", "task", "--description", "Desc", "--assignee", "alice")
		out := bdShowRaw(t, bd, dir, issue.ID, "--long")
		// Long mode should show extra fields
		if !strings.Contains(out, "Long show") {
			t.Errorf("expected title in long output: %s", out)
		}
	})

	// ===== --id flag (for flag-like IDs) =====

	t.Run("show_id_flag", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "ID flag test", "--type", "task")
		out := bdShowRaw(t, bd, dir, "--id", issue.ID, "--short")
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in output via --id flag: %s", out)
		}
	})

	// ===== --refs =====

	t.Run("show_refs", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Refs parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Refs child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		out := bdShowRaw(t, bd, dir, parent.ID, "--refs")
		// --refs should mention the child that references this issue
		if !strings.Contains(out, child.ID) {
			t.Logf("--refs output did not contain child ID %s: %s", child.ID, out)
		}
	})

	// ===== --children =====

	t.Run("show_children", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Children parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "Children child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		out := bdShowRaw(t, bd, dir, parent.ID, "--children")
		if !strings.Contains(out, child.ID) {
			t.Logf("--children output did not contain child ID %s: %s", child.ID, out)
		}
	})

	t.Run("show_children_empty", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No children", "--type", "task")
		// Should not error even with no children
		_ = bdShowRaw(t, bd, dir, issue.ID, "--children")
	})
}

func TestEmbeddedShowDetailAndCurrentHistoryAndCurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ts")

	// ===== --as-of =====

	t.Run("show_as_of", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "AsOf test", "--type", "task")

		// Get current commit hash
		store := openStore(t, beadsDir, "ts")
		commitHash, err := store.GetCurrentCommit(t.Context())
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}
		store.Close() // release flock before subprocess

		// Update the issue
		bdUpdate(t, bd, dir, issue.ID, "--title", "AsOf updated")

		// Show at the old commit — should have original title
		out := bdShowRaw(t, bd, dir, issue.ID, "--as-of", commitHash)
		if !strings.Contains(out, "AsOf test") {
			t.Errorf("expected original title at old commit, got: %s", out)
		}
		if strings.Contains(out, "AsOf updated") {
			t.Errorf("should not see updated title at old commit: %s", out)
		}
	})

	// ===== --local-time =====

	t.Run("show_local_time", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Local time", "--type", "task")
		// Just verify it doesn't error
		out := bdShowRaw(t, bd, dir, issue.ID, "--local-time")
		if !strings.Contains(out, "Local time") {
			t.Errorf("expected title in output: %s", out)
		}
	})

	// ===== --current =====

	t.Run("show_current_fallback_to_last_touched", func(t *testing.T) {
		// After showing issues in earlier tests, --current resolves via last-touched fallback
		out := bdShowRaw(t, bd, dir, "--current")
		if len(out) == 0 {
			t.Error("expected --current to produce output")
		}
	})

	t.Run("show_current_with_in_progress", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "In progress for current", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress", "--assignee", "test@test.com")

		out := bdShowRaw(t, bd, dir, "--current")
		if !strings.Contains(out, "In progress for current") {
			t.Logf("--current did not resolve to in-progress issue: %s", out)
		}
	})

	// ===== view alias =====

	t.Run("view_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "View alias test", "--type", "task")
		cmd := exec.Command(bd, "view", issue.ID, "--short")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd view failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), issue.ID) {
			t.Errorf("expected ID in view alias output: %s", stdout.String())
		}
	})

	// ===== --current with --id conflict =====

	t.Run("show_current_with_id_fails", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Conflict test", "--type", "task")
		bdShowFail2(t, bd, dir, "--current", issue.ID)
	})

	// ===== Epic progress in JSON =====

	t.Run("show_json_epic_progress", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic progress", "--type", "epic")
		child1 := bdCreate(t, bd, dir, "Epic child 1", "--type", "task")
		child2 := bdCreate(t, bd, dir, "Epic child 2", "--type", "task")
		bdDepAdd(t, bd, dir, child1.ID, epic.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, child2.ID, epic.ID, "--type", "parent-child")
		bdClose(t, bd, dir, child1.ID)

		m := bdShowDetails(t, bd, dir, epic.ID)
		if total, ok := m["epic_total_children"]; ok {
			if total != float64(2) {
				t.Errorf("expected epic_total_children=2, got %v", total)
			}
		}
		if closed, ok := m["epic_closed_children"]; ok {
			if closed != float64(1) {
				t.Errorf("expected epic_closed_children=1, got %v", closed)
			}
		}
	})
}

// TestEmbeddedShowAsOfFailureExitCodes pins `bd show --as-of` to plain show's
// failure contract on the direct route. Each lookup that fails is reported on
// stderr and the others still render; the command fails only when no issue
// could be read at the ref, with show's JSON error envelope on stdout under
// --json and a bare exit 1 otherwise. A ref the Dolt AS OF read does not take
// (a versioned-history address, an instant), a branch that does not exist,
// and an issue created after the ref each used to exit 0 with nothing on
// stdout, which a caller cannot tell from a successful read.
func TestEmbeddedShowAsOfFailureExitCodes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sx")

	issue := bdCreate(t, bd, dir, "AsOf original", "--type", "task")
	store := openStore(t, beadsDir, "sx")
	ref, err := store.GetCurrentCommit(t.Context())
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	store.Close() // release flock before subprocess
	later := bdCreate(t, bd, dir, "Created after the ref", "--type", "task")

	show := func(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
		t.Helper()
		cmd := exec.Command(bd, append([]string{"show"}, args...)...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, errOut, err := runCommandBuffers(t, cmd)
		if err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("bd show %s did not run: %v", strings.Join(args, " "), err)
			}
			exitCode = ee.ExitCode()
		}
		return out.String(), errOut.String(), exitCode
	}

	for _, tc := range []struct {
		name string
		args []string
		// why is what the per-id report on stderr has to name.
		why string
	}{
		{"versioned_history_address", []string{issue.ID, "--as-of", "asof:" + issue.ID + ":1"}, "invalid ref"},
		{"instant", []string{issue.ID, "--as-of", "2026-10-17T00:00:00Z"}, "invalid ref"},
		{"unknown_branch", []string{issue.ID, "--as-of", "no-such-branch"}, "no-such-branch"},
		{"issue_created_after_ref", []string{later.ID, "--as-of", ref}, later.ID},
		{"issue_created_after_ref_short", []string{later.ID, "--as-of", ref, "--short"}, later.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := show(t, tc.args...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tc.why) {
				t.Errorf("bd show %s exited %d, want 1 with empty stdout and %q on stderr\nstdout:\n%s\nstderr:\n%s",
					strings.Join(tc.args, " "), code, tc.why, stdout, stderr)
			}

			args := append(append([]string{}, tc.args...), "--json")
			stdout, stderr, code = show(t, args...)
			var envelope struct {
				Error string `json:"error"`
				Hint  string `json:"hint"`
			}
			if code != 1 || json.Unmarshal([]byte(stdout), &envelope) != nil ||
				envelope.Error != "no issues found matching the provided IDs" || envelope.Hint == "" {
				t.Errorf("bd show %s exited %d, want 1 with show's JSON error envelope on stdout\nstdout:\n%s\nstderr:\n%s",
					strings.Join(args, " "), code, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.why) {
				t.Errorf("bd show %s stderr does not name %q:\n%s", strings.Join(args, " "), tc.why, stderr)
			}
		})
	}

	// Plain show's partial failure: the issues that were read are the
	// answer, the miss is reported on stderr, and the command succeeds.
	t.Run("partial_failure_exits_zero", func(t *testing.T) {
		stdout, stderr, code := show(t, issue.ID, later.ID, "--as-of", ref, "--json")
		var shown []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &shown) != nil || len(shown) != 1 ||
			shown[0].ID != issue.ID || shown[0].Title != "AsOf original" {
			t.Errorf("bd show %s %s --as-of %s --json exited %d, want 0 with an array of %s alone\nstdout:\n%s\nstderr:\n%s",
				issue.ID, later.ID, ref, code, issue.ID, stdout, stderr)
		}
		if !strings.Contains(stderr, later.ID) {
			t.Errorf("--json stderr does not report %s:\n%s", later.ID, stderr)
		}

		stdout, stderr, code = show(t, issue.ID, later.ID, "--as-of", ref)
		if code != 0 || !strings.Contains(stdout, "AsOf original") || !strings.Contains(stderr, later.ID) {
			t.Errorf("bd show %s %s --as-of %s exited %d, want 0 rendering %s and reporting %s on stderr\nstdout:\n%s\nstderr:\n%s",
				issue.ID, later.ID, ref, code, issue.ID, later.ID, stdout, stderr)
		}
	})
}

// TestEmbeddedShowConcurrent exercises show operations concurrently.
func TestEmbeddedShowConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sc")

	const (
		numWorkers      = 8
		issuesPerWorker = 3
	)

	// Pre-create issues
	var allIDs []string
	for i := 0; i < numWorkers*issuesPerWorker; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-show-%d", i), "--type", "task")
		allIDs = append(allIDs, issue.ID)
	}

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			for i := 0; i < issuesPerWorker; i++ {
				idx := worker*issuesPerWorker + i
				id := allIDs[idx]

				// Show via JSON
				cmd := exec.Command(bd, "show", id, "--json")
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("show %s: %v\n%s", id, err, out)
					results[worker] = r
					return
				}

				// Show via short
				cmd = exec.Command(bd, "show", id, "--short")
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err = cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("show --short %s: %v\n%s", id, err, out)
					results[worker] = r
					return
				}
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
