//go:build cgo

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Route tests for `bd show --comments-tail N` (#6618 follow-up, bd-6gl9h7):
// the shared fixtures and the proxied routes live here; the direct routes
// are in show_comments_tail_embedded_test.go, which the embedded cmd shard
// manifest discovers by its *_embedded_test.go name.
//
// show_comments_tail_test.go pins printComments and renderWatchComments by
// calling them directly, so a regression that passed 0 at any of the three
// call sites — show.go (direct), show_display.go (direct --watch),
// show_proxied_server.go / show_proxied_watch.go (proxied, plain and
// --watch) — would still pass that file. These tests run the built binary
// through every route with the flag set, over the same three comments, and
// read the rendered text: the elision line names the hidden count and the
// issue, only the newest comment survives the cap, and the flag's off state
// (0, or a cap above the count) is byte-identical to its absence.

const (
	commentsTailOldest = "CT-OLDEST-ALPHA"
	commentsTailOlder  = "CT-OLDER-BRAVO"
	commentsTailNewest = "CT-NEWEST-CHARLIE"
)

var commentsTailTexts = []string{commentsTailOldest, commentsTailOlder, commentsTailNewest}

// commentsTailElision is the one muted line printComments prints in front of
// a capped render; the text is pinned here so a reworded line fails every
// route at once rather than one route drifting.
func commentsTailElision(hidden int, issueID string) string {
	noun := "comments"
	if hidden == 1 {
		noun = "comment"
	}
	return fmt.Sprintf("… %d older %s hidden — bd show %s for the full record", hidden, noun, issueID)
}

// assertCommentsTailOne checks a text render of the three-comment issue
// under --comments-tail 1: the elision line, the newest comment, and neither
// of the two hidden ones.
func assertCommentsTailOne(t *testing.T, route, out, issueID string) {
	t.Helper()
	if want := commentsTailElision(2, issueID); !strings.Contains(out, want) {
		t.Errorf("%s: elision line missing, want %q in:\n%s", route, want, out)
	}
	if !strings.Contains(out, commentsTailNewest) {
		t.Errorf("%s: newest comment %q missing from the capped render:\n%s", route, commentsTailNewest, out)
	}
	for _, hidden := range []string{commentsTailOldest, commentsTailOlder} {
		if strings.Contains(out, hidden) {
			t.Errorf("%s: hidden comment %q rendered under --comments-tail 1:\n%s", route, hidden, out)
		}
	}
}

// assertCommentsTailOff checks that a render with the flag in its off state
// (0, or a cap at or above the count) is byte-identical to a render without
// the flag, and that the uncapped render carries every comment and no elision
// line. Only on the direct route (tips) may the two legitimately differ, by
// the trailing tip block stripTrailingShowTip removes from both first; the
// proxied route never prints a tip, so its renders are compared as they came.
// Nothing else is forgiven: a stray trailing blank line printed only under
// --comments-tail fails here on every route.
func assertCommentsTailOff(t *testing.T, route string, tips bool, withFlag, without string) {
	t.Helper()
	if tips {
		withFlag, without = stripTrailingShowTip(withFlag), stripTrailingShowTip(without)
	}
	if withFlag != without {
		t.Errorf("%s: render differs from the flag's absence\n--- with flag\n%s\n--- without\n%s", route, withFlag, without)
	}
	for _, text := range commentsTailTexts {
		if !strings.Contains(without, text) {
			t.Errorf("%s: uncapped render lacks comment %q:\n%s", route, text, without)
		}
	}
	if strings.Contains(without, "older comment") {
		t.Errorf("%s: uncapped render carries an elision line:\n%s", route, without)
	}
}

// runShowWatchOnce starts `bd show <args>` (the caller passes --watch), waits
// for the first render to land on stdout, stops the watch with SIGINT and
// returns the stdout of that single render. The start / stop choreography is
// startShowWatch + interrupt (show_proxied_watch_integration_test.go), the
// same TestProxiedServerShowWatch runs; the quiet wait in between is why the
// banner alone is not proof that stdout has fully landed.
func runShowWatchOnce(t *testing.T, bd, dir string, env []string, args ...string) string {
	t.Helper()
	w := startShowWatch(t, bd, dir, env, append([]string{"show"}, args...)...)
	w.stdout.waitForQuiet(500*time.Millisecond, 10*time.Second, w.exited)
	w.interrupt(t)
	return w.stdout.String()
}

// TestProxiedServerShowCommentsTail drives the proxied route
// (show_proxied_server.go) and the proxied --watch route
// (show_proxied_watch.go) through the built binary against the shared
// proxied server.
func TestProxiedServerShowCommentsTail(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pct")
	issue := bdProxiedCreate(t, bd, p.dir, "Tail me", "--type", "task")
	for _, text := range commentsTailTexts {
		bdProxiedComment(t, bd, p.dir, issue.ID, text)
	}

	t.Run("proxied_tail_1", func(t *testing.T) {
		t.Parallel()
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--comments-tail", "1")
		assertCommentsTailOne(t, "proxied", out, issue.ID)
	})

	t.Run("proxied_off_state_matches_absence", func(t *testing.T) {
		t.Parallel()
		without := bdProxiedShowRaw(t, bd, p.dir, issue.ID)
		assertCommentsTailOff(t, "proxied --comments-tail 0", false, bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--comments-tail", "0"), without)
		assertCommentsTailOff(t, "proxied --comments-tail 5", false, bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--comments-tail", "5"), without)
	})

	t.Run("proxied_negative_is_usage_error", func(t *testing.T) {
		t.Parallel()
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, issue.ID, "--comments-tail", "-1")
		combined := stdout + stderr
		if !strings.Contains(combined, "--comments-tail") || !strings.Contains(combined, "non-negative") {
			t.Errorf("negative cap did not fail as a usage error\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
	})

	t.Run("proxied_json_untouched", func(t *testing.T) {
		t.Parallel()
		withFlag, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "show", issue.ID, "--json", "--include-comments", "--comments-tail", "1")
		if err != nil {
			t.Fatalf("bd show --json --comments-tail 1 failed: %v\n%s", err, stderr)
		}
		without, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "show", issue.ID, "--json", "--include-comments")
		if err != nil {
			t.Fatalf("bd show --json failed: %v\n%s", err, stderr)
		}
		if withFlag != without {
			t.Errorf("--json output changed under --comments-tail 1\n--- with flag\n%s\n--- without\n%s", withFlag, without)
		}
		if !strings.Contains(withFlag, commentsTailOldest) {
			t.Errorf("--json --include-comments dropped the oldest comment under --comments-tail 1:\n%s", withFlag)
		}
	})

	t.Run("proxied_watch_tail_1", func(t *testing.T) {
		t.Parallel()
		out := runShowWatchOnce(t, bd, p.dir, bdProxiedEnv(p.dir), issue.ID, "--watch", "--comments-tail", "1")
		assertCommentsTailOne(t, "proxied --watch", out, issue.ID)
	})
}
