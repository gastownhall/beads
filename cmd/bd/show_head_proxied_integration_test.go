//go:build cgo

package main

import (
	"encoding/json"
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
// both routes over one fat issue and read the rendered text and the JSON.

const (
	showHeadDescription = "HEAD-DESCRIPTION-MARKER"
	showHeadDesign      = "HEAD-DESIGN-MARKER"
	showHeadAcceptance  = "HEAD-ACCEPTANCE-MARKER"
	showHeadNotesEarly  = "HEAD-NOTES-EARLY-MARKER"
	showHeadNotesLast   = "HEAD-NOTES-LAST-MARKER"
	// showHeadDepBody is the description of the issue the fixture depends
	// on. A full dependency row carries it and a brief one does not.
	showHeadDepBody = "HEAD-DEP-BODY-MARKER"
)

// showHeadDepArgs are the bd create arguments of the issue the fat fixture
// issue depends on.
func showHeadDepArgs() []string {
	return []string{"Head dep", "--type", "task", "--description", showHeadDepBody}
}

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

// showHeadJSON parses the output of bd show --head --json: an array with one
// object per issue, as bd show --json prints.
func showHeadJSON(t *testing.T, route, out string) []map[string]interface{} {
	t.Helper()
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("%s: no JSON array in the output:\n%s", route, out)
	}
	var docs []map[string]interface{}
	if err := json.Unmarshal([]byte(out[start:]), &docs); err != nil {
		t.Fatalf("%s: parse the JSON head view: %v\n%s", route, err, out)
	}
	return docs
}

// showHeadWithheld reads the withheld object of one JSON head view.
func showHeadWithheld(t *testing.T, route string, doc map[string]interface{}) map[string]interface{} {
	t.Helper()
	withheld, ok := doc["withheld"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s: the head view has no withheld object: %v", route, doc)
	}
	for _, key := range []string{"description_bytes", "design_bytes", "notes_bytes", "acceptance_criteria_bytes", "comments"} {
		if _, ok := withheld[key].(float64); !ok {
			t.Errorf("%s: withheld lacks the number %q: %v", route, key, withheld)
		}
	}
	return withheld
}

// assertShowHeadJSON checks the JSON head view of the fixture issue, as the
// only issue in out. shown is how many of the newest comments the view was
// asked for (0 keeps them all), and depID is the issue the fixture depends on.
func assertShowHeadJSON(t *testing.T, route, out, depID string, shown int) {
	t.Helper()
	docs := showHeadJSON(t, route, out)
	if len(docs) != 1 {
		t.Fatalf("%s: %d issues in the view, want 1:\n%s", route, len(docs), out)
	}
	doc := docs[0]
	if doc["title"] != "Head me" {
		t.Errorf("%s: title = %v", route, doc["title"])
	}
	for _, key := range []string{"description", "design", "acceptance_criteria"} {
		if _, ok := doc[key]; ok {
			t.Errorf("%s: the head view carries %q:\n%s", route, key, out)
		}
	}
	// No withheld text anywhere in the document, the dependency rows
	// included: a full dependency row would carry that issue's description.
	for _, text := range []string{showHeadDescription, showHeadDesign, showHeadAcceptance, showHeadNotesEarly, showHeadDepBody} {
		if strings.Contains(out, text) {
			t.Errorf("%s: the head view carries %q:\n%s", route, text, out)
		}
	}

	notes, _ := doc["notes"].(string)
	if !strings.HasSuffix(notes, showHeadNotesLast) || len(notes) > 1024 {
		t.Errorf("%s: notes are not the last 1 KB of the field (%d bytes):\n%s", route, len(notes), notes)
	}

	hidden := 0
	if shown > 0 && shown < len(showHeadComments) {
		hidden = len(showHeadComments) - shown
	}
	var got []string
	comments, _ := doc["comments"].([]interface{})
	for _, c := range comments {
		row, _ := c.(map[string]interface{})
		text, _ := row["text"].(string)
		got = append(got, text)
	}
	if want := showHeadComments[hidden:]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: comments = %v, want %v", route, got, want)
	}
	if doc["comment_count"] != float64(len(showHeadComments)) {
		t.Errorf("%s: comment_count = %v, want the count of all %d", route, doc["comment_count"], len(showHeadComments))
	}

	deps, _ := doc["dependencies"].([]interface{})
	if len(deps) != 1 {
		t.Fatalf("%s: %d dependency rows, want 1:\n%s", route, len(deps), out)
	}
	if dep, _ := deps[0].(map[string]interface{}); dep["id"] != depID || dep["title"] != "Head dep" {
		t.Errorf("%s: the dependency row lost its identity: %v", route, deps[0])
	}

	withheld := showHeadWithheld(t, route, doc)
	want := map[string]int{
		"description_bytes":         len(showHeadDescription),
		"design_bytes":              len(showHeadDesign),
		"notes_bytes":               len(showHeadNotes()) - len(notes),
		"acceptance_criteria_bytes": len(showHeadAcceptance),
		"comments":                  hidden,
	}
	for key, n := range want {
		if withheld[key] != float64(n) {
			t.Errorf("%s: withheld.%s = %v, want %d", route, key, withheld[key], n)
		}
	}
}

// assertShowHeadJSONOff checks that bd show --json without --head still
// answers as it did: the content fields whole, the full dependency row, no
// comment bodies and no withheld object.
func assertShowHeadJSONOff(t *testing.T, route string, doc map[string]interface{}) {
	t.Helper()
	if doc["description"] != showHeadDescription || doc["design"] != showHeadDesign || doc["acceptance_criteria"] != showHeadAcceptance || doc["notes"] != showHeadNotes() {
		t.Errorf("%s: bd show --json lost or cut a content field: %v", route, doc)
	}
	if _, ok := doc["withheld"]; ok {
		t.Errorf("%s: bd show --json carries a withheld object without --head", route)
	}
	if _, ok := doc["comments"]; ok || doc["comments_omitted"] != true {
		t.Errorf("%s: bd show --json reads comment bodies without --include-comments: comments_omitted=%v", route, doc["comments_omitted"])
	}
	deps, _ := doc["dependencies"].([]interface{})
	if len(deps) != 1 {
		t.Fatalf("%s: %d dependency rows, want 1", route, len(deps))
	}
	if dep, _ := deps[0].(map[string]interface{}); dep["description"] != showHeadDepBody {
		t.Errorf("%s: bd show --json reduced the dependency row without --brief-deps: %v", route, deps[0])
	}
}

// showHeadRefused are the flags --head is refused with, as a test passes
// them; the refusal names the flag without its value.
var showHeadRefused = []string{"--short", "--long", "--refs", "--children", "--thread", "--watch", "--as-of=main", "--include-comments"}

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
	dep := bdProxiedCreate(t, bd, p.dir, showHeadDepArgs()...)
	bdProxiedDep(t, bd, p.dir, "add", issue.ID, dep.ID)

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

	t.Run("proxied_head_json", func(t *testing.T) {
		t.Parallel()
		assertShowHeadJSON(t, "proxied", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head", "--json"), dep.ID, 3)
	})

	t.Run("proxied_head_json_with_comments_tail", func(t *testing.T) {
		t.Parallel()
		assertShowHeadJSON(t, "proxied --comments-tail 1", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head", "--json", "--comments-tail", "1"), dep.ID, 1)
		assertShowHeadJSON(t, "proxied --comments-tail 0", bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--head", "--json", "--comments-tail", "0"), dep.ID, 0)
	})

	t.Run("proxied_full_json_untouched", func(t *testing.T) {
		t.Parallel()
		assertShowHeadJSONOff(t, "proxied", bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID))
	})

	t.Run("proxied_head_json_one_object_per_issue", func(t *testing.T) {
		t.Parallel()
		docs := showHeadJSON(t, "proxied", bdProxiedShowRaw(t, bd, p.dir, issue.ID, dep.ID, "--head", "--json"))
		if len(docs) != 2 {
			t.Fatalf("%d issues in the view, want 2", len(docs))
		}
		// The dependency is a short issue: its description is withheld and
		// nothing else is.
		withheld := showHeadWithheld(t, "proxied", docs[1])
		if docs[1]["id"] != dep.ID || withheld["description_bytes"] != float64(len(showHeadDepBody)) || withheld["notes_bytes"] != float64(0) || withheld["comments"] != float64(0) {
			t.Errorf("the second issue's head view: id=%v withheld=%v", docs[1]["id"], withheld)
		}
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
