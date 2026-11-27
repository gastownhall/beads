//go:build cgo

package main

import (
	"fmt"
	"strings"
	"testing"
)

// Route tests for `bd show --head` (GH#7187): the shared fixtures and the
// proxied route live here; the direct route is in
// show_head_embedded_test.go, whose TestEmbedded* name puts it in the
// embedded cmd shard manifest.
//
// show_head_test.go pins the helpers by calling them directly, so a route
// that never reads the flag, or reads it and still prints the four content
// fields, would pass that file. These tests run the built binary through
// both routes over one fat issue and read the rendered text.

const (
	showHeadDescription = "HEAD-DESCRIPTION-MARKER"
	showHeadDesign      = "HEAD-DESIGN-MARKER"
	showHeadAcceptance  = "HEAD-ACCEPTANCE-MARKER"
	showHeadNotesEarly  = "HEAD-NOTES-EARLY-MARKER"
	showHeadNotesLast   = "HEAD-NOTES-LAST-MARKER"
)

// showHeadComments are the fixture's comments, oldest first. Five, so the
// head view's default of three hides two.
var showHeadComments = []string{"HC-ONE-ALPHA", "HC-TWO-BRAVO", "HC-THREE-CHARLIE", "HC-FOUR-DELTA", "HC-FIVE-ECHO"}

// showHeadNotes is a notes field of about 4 KB whose first and last lines
// are distinguishable, so a view of its end shows one and not the other.
func showHeadNotes() string {
	return showHeadNotesEarly + "\n" + strings.Repeat("a filler line of the notes field, appended over months\n", 80) + showHeadNotesLast
}

// showHeadCreateArgs are the bd create arguments of the fat fixture issue.
func showHeadCreateArgs() []string {
	return []string{"Head me", "--type", "task",
		"--description", showHeadDescription,
		"--design", showHeadDesign,
		"--acceptance", showHeadAcceptance,
		"--notes", showHeadNotes()}
}

// assertShowHead checks a --head render of the fixture issue. shown is how
// many of the newest comments the render was asked for (0 renders them all).
func assertShowHead(t *testing.T, route, out, issueID string, shown int) {
	t.Helper()
	if !strings.Contains(out, "Head me") {
		t.Errorf("%s: the header lost the title:\n%s", route, out)
	}
	for _, withheld := range []string{showHeadDescription, showHeadDesign, showHeadAcceptance, showHeadNotesEarly, "DESCRIPTION", "ACCEPTANCE CRITERIA"} {
		if strings.Contains(out, withheld) {
			t.Errorf("%s: the head view printed %q:\n%s", route, withheld, out)
		}
	}
	if !strings.Contains(out, showHeadNotesLast) {
		t.Errorf("%s: the last line of the notes is missing:\n%s", route, out)
	}
	if !strings.Contains(out, "of earlier notes withheld") {
		t.Errorf("%s: the notes were clipped without saying so:\n%s", route, out)
	}

	hidden := 0
	if shown > 0 && shown < len(showHeadComments) {
		hidden = len(showHeadComments) - shown
	}
	for i, text := range showHeadComments {
		if got, want := strings.Contains(out, text), i >= hidden; got != want {
			t.Errorf("%s: comment %q rendered=%v, want %v with %d shown:\n%s", route, text, got, want, shown, out)
		}
	}
	if !strings.Contains(out, "… head view: withheld description (") {
		t.Errorf("%s: the trailer does not name the withheld description:\n%s", route, out)
	}
	if !strings.Contains(out, fmt.Sprintf("bd show %s for the full record", issueID)) {
		t.Errorf("%s: the trailer does not point at the full record:\n%s", route, out)
	}
	// With none hidden the view must not mention older comments at all,
	// neither in the COMMENTS elision line nor in the trailer.
	older := "older comment"
	if hidden > 1 {
		older = fmt.Sprintf(", %d older comments — bd show", hidden)
	} else if hidden == 1 {
		older = ", 1 older comment — bd show"
	}
	if got, want := strings.Contains(out, older), hidden > 0; got != want {
		t.Errorf("%s: view names hidden comments=%v, want %v (%d hidden):\n%s", route, got, want, hidden, out)
	}
}

// showHeadRefused are the flags --head is refused with, as a test passes
// them; the refusal names the flag without its value.
var showHeadRefused = []string{"--json", "--short", "--long", "--refs", "--children", "--thread", "--watch", "--as-of=main"}

// showHeadRefusal is the text the refusal of --head with other must carry.
func showHeadRefusal(other string) string {
	name, _, _ := strings.Cut(other, "=")
	return "cannot be combined with " + name
}

// assertShowHeadOff checks that the full view of the fixture issue still
// prints every field and every comment, and no head-view line.
func assertShowHeadOff(t *testing.T, route, out string) {
	t.Helper()
	want := append([]string{showHeadDescription, showHeadDesign, showHeadAcceptance, showHeadNotesEarly, showHeadNotesLast}, showHeadComments...)
	for _, text := range want {
		if !strings.Contains(out, text) {
			t.Errorf("%s: the full view lacks %q:\n%s", route, text, out)
		}
	}
	for _, line := range []string{"head view", "notes withheld"} {
		if strings.Contains(out, line) {
			t.Errorf("%s: the full view carries a head-view line (%q):\n%s", route, line, out)
		}
	}
}

// TestProxiedServerShowHead drives the proxied route (show_proxied_server.go)
// through the built binary against the shared proxied server.
func TestProxiedServerShowHead(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "phd")
	issue := bdProxiedCreate(t, bd, p.dir, showHeadCreateArgs()...)
	for _, text := range showHeadComments {
		bdProxiedComment(t, bd, p.dir, issue.ID, text)
	}

	t.Run("proxied_head", func(t *testing.T) {
		t.Parallel()
		assertShowHead(t, "proxied", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head"), issue.ID, 3)
	})

	t.Run("proxied_head_with_comments_tail", func(t *testing.T) {
		t.Parallel()
		assertShowHead(t, "proxied --comments-tail 1", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head", "--comments-tail", "1"), issue.ID, 1)
		assertShowHead(t, "proxied --comments-tail 0", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head", "--comments-tail", "0"), issue.ID, 0)
	})

	t.Run("proxied_full_view_untouched", func(t *testing.T) {
		t.Parallel()
		assertShowHeadOff(t, "proxied", bdProxiedShowRaw(t, bd, p.dir, issue.ID))
	})

	t.Run("proxied_short_issue_withholds_nothing", func(t *testing.T) {
		t.Parallel()
		short := bdProxiedCreate(t, bd, p.dir, "Short one", "--type", "task", "--notes", "one line of state")
		out := bdProxiedShowRaw(t, bd, p.dir, short.ID, "--head")
		if !strings.Contains(out, "… head view: nothing withheld") || !strings.Contains(out, "one line of state") {
			t.Errorf("a short issue did not render whole with the nothing-withheld line:\n%s", out)
		}
	})

	t.Run("proxied_refuses_other_views", func(t *testing.T) {
		t.Parallel()
		for _, other := range showHeadRefused {
			stdout, stderr := bdProxiedShowFail(t, bd, p.dir, issue.ID, "--head", other)
			if combined := stdout + stderr; !strings.Contains(combined, "--head") || !strings.Contains(combined, showHeadRefusal(other)) {
				t.Errorf("--head %s did not fail as a usage error\nstdout:\n%s\nstderr:\n%s", other, stdout, stderr)
			}
		}
	})
}
