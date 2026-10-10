//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// Direct-route half of the --comments-tail route tests; fixtures and the
// proxied half are in show_comments_tail_proxied_integration_test.go.

// TestEmbeddedShowCommentsTail drives the direct route (show.go) and the
// direct --watch route (show_display.go) through the built binary.
func TestEmbeddedShowCommentsTail(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ct")
	issue := bdCreate(t, bd, dir, "Tail me", "--type", "task")
	for _, text := range commentsTailTexts {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "comments", "add", issue.ID, text); err != nil {
			t.Fatalf("bd comments add %q failed: %v\n%s", text, err, out)
		}
	}

	t.Run("direct_tail_1", func(t *testing.T) {
		out := bdShowRaw(t, bd, dir, issue.ID, "--comments-tail", "1")
		assertCommentsTailOne(t, "direct", out, issue.ID)
	})

	t.Run("direct_off_state_matches_absence", func(t *testing.T) {
		without := bdShowRaw(t, bd, dir, issue.ID)
		assertCommentsTailOff(t, "direct --comments-tail 0", true, bdShowRaw(t, bd, dir, issue.ID, "--comments-tail", "0"), without)
		assertCommentsTailOff(t, "direct --comments-tail 5", true, bdShowRaw(t, bd, dir, issue.ID, "--comments-tail", "5"), without)
	})

	t.Run("direct_negative_is_usage_error", func(t *testing.T) {
		out := bdShowFail2(t, bd, dir, issue.ID, "--comments-tail", "-1")
		if !strings.Contains(out, "--comments-tail") || !strings.Contains(out, "non-negative") {
			t.Errorf("negative cap did not fail as a usage error:\n%s", out)
		}
	})

	t.Run("direct_json_untouched", func(t *testing.T) {
		withFlag := bdShowRaw(t, bd, dir, issue.ID, "--json", "--include-comments", "--comments-tail", "1")
		without := bdShowRaw(t, bd, dir, issue.ID, "--json", "--include-comments")
		if withFlag != without {
			t.Errorf("--json output changed under --comments-tail 1\n--- with flag\n%s\n--- without\n%s", withFlag, without)
		}
		if !strings.Contains(withFlag, commentsTailOldest) {
			t.Errorf("--json --include-comments dropped the oldest comment under --comments-tail 1:\n%s", withFlag)
		}
	})

	t.Run("watch_tail_1", func(t *testing.T) {
		out := runShowWatchOnce(t, bd, dir, bdEnv(dir), issue.ID, "--watch", "--comments-tail", "1")
		assertCommentsTailOne(t, "direct --watch", out, issue.ID)
	})
}
