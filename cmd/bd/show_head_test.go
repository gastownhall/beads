package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

// Unit tests for the helpers behind `bd show --head` (GH#7187). They call
// the helpers directly, so they cannot see a route that forgets to call them:
// TestEmbeddedShowHead and TestProxiedServerShowHead run the built binary
// through both routes for that.

// setShowFlag sets one showCmd flag for a test and restores its default and
// its Changed bit afterwards, so the shared command does not leak state into
// the next test.
func setShowFlag(t *testing.T, name, value string) {
	t.Helper()
	flag := showCmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("show has no --%s flag", name)
	}
	if err := showCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = showCmd.Flags().Set(name, flag.DefValue)
		flag.Changed = false
	})
}

func TestShowHeadFlagRegistration(t *testing.T) {
	flag := showCmd.Flags().Lookup("head")
	if flag == nil {
		t.Fatal("head flag is not registered on showCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("head default = %q, want \"false\": the default output must not change", flag.DefValue)
	}
	if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); in.head {
		t.Error("the proxied route read head=true with the flag unset")
	}
	setShowFlag(t, "head", "true")
	if in := gatherShowProxiedInput(showCmd, []string{"be-abc"}); !in.head {
		t.Error("gatherShowProxiedInput did not carry --head to the proxied request")
	}
}

func TestClipNotesTail(t *testing.T) {
	line := func(i int) string { return fmt.Sprintf("line %03d of the notes field\n", i) }
	var many strings.Builder
	for i := 0; i < 200; i++ {
		many.WriteString(line(i))
	}
	lines := many.String() + "LAST-LINE"

	t.Run("notes within the budget are returned whole", func(t *testing.T) {
		for _, notes := range []string{"", "short", strings.Repeat("x", 1024)} {
			tail, withheld := clipNotesTail(notes, 1024, 256)
			if tail != notes || withheld != 0 {
				t.Errorf("len %d: got tail len %d, withheld %d; want the whole field and 0", len(notes), len(tail), withheld)
			}
		}
	})

	t.Run("keeps the end and starts on a line", func(t *testing.T) {
		tail, withheld := clipNotesTail(lines, 1024, 256)
		if !strings.HasSuffix(lines, tail) || withheld+len(tail) != len(lines) {
			t.Fatalf("tail is not the end of the field: withheld %d + tail %d != %d", withheld, len(tail), len(lines))
		}
		if len(tail) > 1024 {
			t.Errorf("tail is %d bytes, over the 1024 budget", len(tail))
		}
		if !strings.HasSuffix(tail, "LAST-LINE") {
			t.Errorf("tail lost the last line: %q", tail[len(tail)-20:])
		}
		if !strings.HasPrefix(tail, "line ") || lines[withheld-1] != '\n' {
			t.Errorf("tail does not start on a line boundary: %q", tail[:30])
		}
		if strings.Contains(tail, "line 000") {
			t.Error("tail still carries the first line of a 5 KB field")
		}
	})

	t.Run("a cut that lands on a line start keeps that line", func(t *testing.T) {
		notes := strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 49) + "\n" + strings.Repeat("c", 49) + "\n"
		tail, withheld := clipNotesTail(notes, 100, 25)
		if withheld != 101 || !strings.HasPrefix(tail, strings.Repeat("b", 49)) {
			t.Errorf("withheld %d, tail %q; want the cut left at the start of the b line", withheld, tail)
		}
	})

	t.Run("one line longer than the budget is cut at the budget", func(t *testing.T) {
		tail, withheld := clipNotesTail(strings.Repeat("z", 5000), 1024, 256)
		if len(tail) != 1024 || withheld != 5000-1024 {
			t.Errorf("tail %d bytes, withheld %d; want 1024 and %d", len(tail), withheld, 5000-1024)
		}
	})

	t.Run("a long paragraph before a short last line is not given up", func(t *testing.T) {
		// Moving to the only line start would leave "ok" out of a 1 KB
		// budget, so the cut stays inside the paragraph.
		notes := strings.Repeat("p", 3000) + "\nok"
		tail, withheld := clipNotesTail(notes, 1024, 256)
		if len(tail) != 1024 || withheld != len(notes)-1024 || !strings.HasSuffix(tail, "\nok") {
			t.Errorf("tail %d bytes, withheld %d; want the last 1024 bytes of the field", len(tail), withheld)
		}
	})

	t.Run("a line start within the slack is taken, one past it is not", func(t *testing.T) {
		at := func(partial int) int {
			notes := strings.Repeat("a", 2000) + strings.Repeat("b", partial) + "\n" + strings.Repeat("c", 1023-partial)
			_, withheld := clipNotesTail(notes, 1024, 256)
			return withheld
		}
		if got := at(255); got != 2000+255+1 {
			t.Errorf("partial line of 255 bytes: withheld %d, want the cut moved to the line start at %d", got, 2000+255+1)
		}
		if got := at(256); got != 2000 {
			t.Errorf("partial line of 256 bytes: withheld %d, want the cut left at 2000", got)
		}
	})

	t.Run("a newline followed only by blank space does not empty the view", func(t *testing.T) {
		// The cut lands at 1977, 23 bytes before the newline and so within
		// the slack, but only blank space follows that line start: the cut
		// stays where it landed instead of moving past the last text.
		notes := strings.Repeat("q", 2000) + "\n" + strings.Repeat(" ", 1000)
		tail, withheld := clipNotesTail(notes, 1024, 256)
		if withheld != 1977 || strings.TrimSpace(tail) == "" {
			t.Errorf("withheld %d, tail text %q; want the cut left at 1977, keeping the last text", withheld, strings.TrimSpace(tail))
		}
	})

	t.Run("never splits a character", func(t *testing.T) {
		// "é" is two bytes, so one of two neighbouring budgets has to land
		// inside a character.
		notes := strings.Repeat("é", 1500)
		for _, budget := range []int{1023, 1024} {
			tail, withheld := clipNotesTail(notes, budget, 256)
			if !utf8.ValidString(tail) {
				t.Errorf("budget %d: tail is not valid UTF-8", budget)
			}
			if len(tail) > budget || withheld+len(tail) != len(notes) {
				t.Errorf("budget %d: tail %d bytes, withheld %d of %d", budget, len(tail), withheld, len(notes))
			}
		}
	})
}

func TestPrintHeadNotes(t *testing.T) {
	t.Run("a clipped field names the bytes withheld and keeps the last line", func(t *testing.T) {
		notes := "HEAD-EARLY-MARKER\n" + strings.Repeat("filler line of notes text\n", 200) + "HEAD-LAST-MARKER"
		_, withheld := clipNotesTail(notes, headNotesBytes, headLineSlack)
		out := captureStdout(t, func() error {
			printHeadNotes(notes)
			return nil
		})
		if want := fmt.Sprintf("… %s of earlier notes withheld", formatBytes(int64(withheld))); !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
		if !strings.Contains(out, "HEAD-LAST-MARKER") {
			t.Errorf("the last line of the notes is missing:\n%s", out)
		}
		if strings.Contains(out, "HEAD-EARLY-MARKER") {
			t.Errorf("the start of a 5 KB notes field was printed:\n%s", out)
		}
	})

	t.Run("a field within the budget prints as bd show prints it", func(t *testing.T) {
		notes := "a short maintained paragraph"
		got := captureStdout(t, func() error {
			printHeadNotes(notes)
			return nil
		})
		want := captureStdout(t, func() error {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), uimd.RenderMarkdown(notes))
			return nil
		})
		if got != want {
			t.Errorf("short notes render differs from the full view\n--- head\n%q\n--- full\n%q", got, want)
		}
	})

	t.Run("empty notes print nothing", func(t *testing.T) {
		if out := captureStdout(t, func() error {
			printHeadNotes("")
			return nil
		}); out != "" {
			t.Errorf("empty notes printed %q", out)
		}
	})
}

func TestPrintHeadTrailer(t *testing.T) {
	trailer := func(issue *types.Issue, commentCount, tailN int) string {
		return captureStdout(t, func() error {
			printHeadTrailer(issue, commentCount, tailN, false)
			return nil
		})
	}

	t.Run("the notes figure is the one the NOTES section printed", func(t *testing.T) {
		// 200-byte lines and a 34-byte last line put the byte cut 10 bytes
		// into a line, so the cut moves 190 bytes to the next line start and
		// the withheld size no longer prints as the field's length less the
		// budget would.
		notes := strings.Repeat(strings.Repeat("n", 199)+"\n", 20) + strings.Repeat("L", 34)
		_, withheld := clipNotesTail(notes, headNotesBytes, headLineSlack)
		size := formatBytes(int64(withheld))
		if naive := formatBytes(int64(len(notes) - headNotesBytes)); size == naive {
			t.Fatalf("fixture does not tell the two counts apart: both print as %s", size)
		}
		section := captureStdout(t, func() error {
			printHeadNotes(notes)
			return nil
		})
		if !strings.Contains(section, "… "+size+" of earlier notes withheld") {
			t.Fatalf("NOTES section does not name %s:\n%s", size, section)
		}
		if out := trailer(&types.Issue{ID: "be-abc", Notes: notes}, 0, 3); !strings.Contains(out, "withheld earlier notes ("+size+") — bd show") {
			t.Errorf("trailer does not name the %s the NOTES section named: %q", size, out)
		}
	})

	t.Run("comments that could not be read are not reported as nothing withheld", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printHeadTrailer(&types.Issue{ID: "be-abc"}, 0, 3, true)
			return nil
		})
		if strings.Contains(out, "nothing withheld") || !strings.Contains(out, "withheld the comments (they could not be read) — bd show be-abc") {
			t.Errorf("want the trailer to name the unread comments, got %q", out)
		}
	})

	t.Run("names every withheld part in field order", func(t *testing.T) {
		issue := &types.Issue{
			ID:                 "be-abc",
			Description:        strings.Repeat("d", 2048),
			Design:             strings.Repeat("g", 300),
			Notes:              strings.Repeat("n", 1024+512),
			AcceptanceCriteria: "done when green",
		}
		out := trailer(issue, 17, 3)
		want := "… head view: withheld description (2.0 KB), design (300 B), earlier notes (512 B), acceptance criteria (15 B), 14 older comments — bd show be-abc for the full record"
		if !strings.Contains(out, want) {
			t.Errorf("trailer mismatch\nwant %q\n got %q", want, out)
		}
	})

	t.Run("one hidden comment is singular", func(t *testing.T) {
		out := trailer(&types.Issue{ID: "be-abc"}, 4, 3)
		if !strings.Contains(out, "withheld 1 older comment — bd show") {
			t.Errorf("want a singular comment count, got %q", out)
		}
	})

	t.Run("an uncapped comment list withholds none", func(t *testing.T) {
		for _, tailN := range []int{0, 5, 9} {
			if out := trailer(&types.Issue{ID: "be-abc"}, 5, tailN); !strings.Contains(out, "… head view: nothing withheld") {
				t.Errorf("tail %d of 5 comments: want nothing withheld, got %q", tailN, out)
			}
		}
	})

	t.Run("a short issue says nothing was withheld", func(t *testing.T) {
		out := trailer(&types.Issue{ID: "be-abc", Notes: "short"}, 2, 3)
		if !strings.Contains(out, "… head view: nothing withheld") || strings.Contains(out, "bd show") {
			t.Errorf("want the nothing-withheld line alone, got %q", out)
		}
	})

	t.Run("an issue ID with shell syntax is not pasted into the command", func(t *testing.T) {
		out := trailer(&types.Issue{ID: "be-abc; rm -rf ~", Description: "x"}, 0, 3)
		if strings.Contains(out, "bd show be-abc; rm") {
			t.Errorf("the raw ID went into the suggested command: %q", out)
		}
	})
}

func TestShowHeadConflict(t *testing.T) {
	if got := showHeadConflict(showCmd); got != "" {
		t.Fatalf("no other flag set, want no conflict, got %q", got)
	}
	t.Run("json output is not a conflict", func(t *testing.T) {
		oldJSON := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = oldJSON })
		var err error
		stderr := captureStderr(t, func() { err = validateShowHead(showCmd) })
		if err != nil || stderr != "" {
			t.Errorf("--head with JSON output must be accepted, got err=%v stderr=%q", err, stderr)
		}
	})
	for _, name := range []string{"short", "long", "refs", "children", "thread", "watch", "include-comments"} {
		t.Run(name, func(t *testing.T) {
			setShowFlag(t, name, "true")
			if got := showHeadConflict(showCmd); got != name {
				t.Errorf("want conflict %q, got %q", name, got)
			}
		})
	}
	t.Run("as-of", func(t *testing.T) {
		setShowFlag(t, "as-of", "main")
		if got := showHeadConflict(showCmd); got != "as-of" {
			t.Errorf("want conflict \"as-of\", got %q", got)
		}
	})

	t.Run("the refusal names the flag", func(t *testing.T) {
		setShowFlag(t, "long", "true")
		var err error
		stderr := captureStderr(t, func() { err = validateShowHead(showCmd) })
		if err == nil || !strings.Contains(stderr, "--head cannot be combined with --long") {
			t.Errorf("want a usage error naming --long, got err=%v stderr=%q", err, stderr)
		}
	})

	t.Run("the refusal of --include-comments names the flag that sets the count", func(t *testing.T) {
		setShowFlag(t, "include-comments", "true")
		var err error
		stderr := captureStderr(t, func() { err = validateShowHead(showCmd) })
		if err == nil || !strings.Contains(stderr, "--head cannot be combined with --include-comments") || !strings.Contains(stderr, "--comments-tail N") {
			t.Errorf("want a usage error naming --include-comments and --comments-tail, got err=%v stderr=%q", err, stderr)
		}
	})
}

func TestResolveHeadCommentsTail(t *testing.T) {
	if got := resolveHeadCommentsTail(showCmd, 0); got != headCommentsTail {
		t.Errorf("without --comments-tail the head view renders %d comments, want %d", got, headCommentsTail)
	}
	for _, n := range []string{"0", "7"} {
		t.Run("explicit "+n, func(t *testing.T) {
			setShowFlag(t, "comments-tail", n)
			in, _ := showCmd.Flags().GetInt("comments-tail")
			if got := resolveHeadCommentsTail(showCmd, in); got != in {
				t.Errorf("an explicit --comments-tail %s was replaced by %d", n, got)
			}
		})
	}
}

// headDetailsFixture is a fat detail view as the reader role answers it for
// the JSON head view: every content field set, notes several times the
// budget with distinguishable first and last lines, and five comments.
func headDetailsFixture() *types.IssueDetails {
	count := int64(5)
	details := &types.IssueDetails{
		Issue: types.Issue{
			ID:                 "be-abc",
			Title:              "Head me",
			Description:        strings.Repeat("d", 2048),
			Design:             strings.Repeat("g", 300),
			Notes:              "EARLY-LINE\n" + strings.Repeat("a line of the notes field, appended over months\n", 60) + "LAST-LINE",
			AcceptanceCriteria: "done when green",
		},
		Labels:       []string{"lane:beads"},
		Dependencies: []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "be-dep", Title: "A dependency"}}},
		CommentCount: &count,
	}
	for i := 1; i <= 5; i++ {
		details.Comments = append(details.Comments, &types.Comment{Text: fmt.Sprintf("comment-%d", i)})
	}
	return details
}

func TestCountHeadWithheld(t *testing.T) {
	issue := &headDetailsFixture().Issue
	_, notesCut := clipNotesTail(issue.Notes, headNotesBytes, headLineSlack)
	if notesCut == 0 || notesCut == len(issue.Notes) {
		t.Fatalf("fixture notes are not clipped part-way: cut %d of %d", notesCut, len(issue.Notes))
	}

	t.Run("each field is counted under its own name", func(t *testing.T) {
		got := countHeadWithheld(issue, 17, 3)
		want := headWithheldCounts{DescriptionBytes: 2048, DesignBytes: 300, NotesBytes: notesCut, AcceptanceCriteriaBytes: 15, Comments: 14}
		if got != want {
			t.Errorf("countHeadWithheld = %+v, want %+v", got, want)
		}
	})

	t.Run("an uncapped comment list withholds none", func(t *testing.T) {
		for _, tailN := range []int{0, 5, 9} {
			if got := countHeadWithheld(issue, 5, tailN).Comments; got != 0 {
				t.Errorf("tail %d of 5 comments withheld %d, want 0", tailN, got)
			}
		}
	})

	t.Run("a short issue withholds nothing", func(t *testing.T) {
		if got := countHeadWithheld(&types.Issue{ID: "be-abc", Notes: "short"}, 2, 3); got != (headWithheldCounts{}) {
			t.Errorf("a short issue withheld %+v, want all zero", got)
		}
	})
}

func TestNewHeadDetails(t *testing.T) {
	// encode returns the head view as a JSON reader sees it.
	encode := func(t *testing.T, head *headDetails) map[string]json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(head)
		if err != nil {
			t.Fatalf("marshal the head view: %v", err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("the head view is not one JSON object: %v\n%s", err, raw)
		}
		return doc
	}
	texts := func(comments []*types.Comment) string {
		var out []string
		for _, c := range comments {
			out = append(out, c.Text)
		}
		return strings.Join(out, ",")
	}

	t.Run("cuts the four content fields and keeps the newest comments", func(t *testing.T) {
		details := headDetailsFixture()
		wantNotes, notesCut := clipNotesTail(details.Notes, headNotesBytes, headLineSlack)
		head := newHeadDetails(details, 3)

		if head.Description != "" || head.Design != "" || head.AcceptanceCriteria != "" {
			t.Errorf("a withheld field is still set: description=%d design=%d acceptance=%d bytes",
				len(head.Description), len(head.Design), len(head.AcceptanceCriteria))
		}
		if head.Notes != wantNotes || !strings.HasSuffix(head.Notes, "LAST-LINE") || strings.Contains(head.Notes, "EARLY-LINE") {
			t.Errorf("notes are not the end of the field: %d bytes, want the last %d", len(head.Notes), len(wantNotes))
		}
		if got := texts(head.Comments); got != "comment-3,comment-4,comment-5" {
			t.Errorf("comments = %s, want the newest three in order", got)
		}
		want := headWithheldCounts{DescriptionBytes: 2048, DesignBytes: 300, NotesBytes: notesCut, AcceptanceCriteriaBytes: 15, Comments: 2}
		if head.Withheld != want {
			t.Errorf("withheld = %+v, want %+v", head.Withheld, want)
		}
		if head.ID != "be-abc" || head.Title != "Head me" || len(head.Labels) != 1 || len(head.Dependencies) != 1 || head.CommentCount == nil || *head.CommentCount != 5 {
			t.Errorf("the rest of the answer did not carry over: %+v", head.IssueDetails)
		}
	})

	t.Run("leaves the reader's answer as it was", func(t *testing.T) {
		details := headDetailsFixture()
		notes := details.Notes
		newHeadDetails(details, 1)
		if len(details.Description) != 2048 || len(details.Design) != 300 || details.AcceptanceCriteria == "" || details.Notes != notes {
			t.Error("newHeadDetails rewrote a content field of the details it was given")
		}
		if got := texts(details.Comments); got != "comment-1,comment-2,comment-3,comment-4,comment-5" {
			t.Errorf("newHeadDetails changed the comments it was given: %s", got)
		}
	})

	t.Run("a cap of 0 or one past the count keeps every comment", func(t *testing.T) {
		for _, tailN := range []int{0, 5, 9} {
			head := newHeadDetails(headDetailsFixture(), tailN)
			if len(head.Comments) != 5 || head.Withheld.Comments != 0 {
				t.Errorf("tail %d: %d comments kept and %d withheld, want 5 and 0", tailN, len(head.Comments), head.Withheld.Comments)
			}
		}
	})

	t.Run("an issue with no comments has none to cut", func(t *testing.T) {
		head := newHeadDetails(&types.IssueDetails{Issue: types.Issue{ID: "be-abc"}}, 3)
		if len(head.Comments) != 0 || head.Withheld.Comments != 0 {
			t.Errorf("no comments in, %d out and %d withheld", len(head.Comments), head.Withheld.Comments)
		}
		if _, ok := encode(t, head)["comments"]; ok {
			t.Error("an issue with no comments encoded a comments key")
		}
	})

	t.Run("encodes as the detail object with a withheld object beside its fields", func(t *testing.T) {
		doc := encode(t, newHeadDetails(headDetailsFixture(), 3))
		for _, key := range []string{"description", "design", "acceptance_criteria"} {
			if _, ok := doc[key]; ok {
				t.Errorf("the head view encodes %q, which it withholds", key)
			}
		}
		for _, key := range []string{"id", "title", "notes", "labels", "dependencies", "comments", "comment_count", "withheld"} {
			if _, ok := doc[key]; !ok {
				t.Errorf("the head view lacks the top-level key %q: %v", key, doc)
			}
		}
		var withheld map[string]int
		if err := json.Unmarshal(doc["withheld"], &withheld); err != nil {
			t.Fatalf("withheld is not an object of numbers: %v\n%s", err, doc["withheld"])
		}
		if len(withheld) != 5 || withheld["description_bytes"] != 2048 || withheld["design_bytes"] != 300 ||
			withheld["notes_bytes"] == 0 || withheld["acceptance_criteria_bytes"] != 15 || withheld["comments"] != 2 {
			t.Errorf("withheld = %v", withheld)
		}
	})

	t.Run("no details encode as null, as without --head", func(t *testing.T) {
		raw, err := json.Marshal([]interface{}{newHeadDetails(nil, 3)})
		if err != nil || string(raw) != "[null]" {
			t.Errorf("nil details encoded as %s (err=%v), want [null]", raw, err)
		}
	})

	t.Run("an issue shown whole still carries withheld, all zero", func(t *testing.T) {
		doc := encode(t, newHeadDetails(&types.IssueDetails{Issue: types.Issue{ID: "be-abc", Notes: "short"}}, 3))
		want := `{"description_bytes":0,"design_bytes":0,"notes_bytes":0,"acceptance_criteria_bytes":0,"comments":0}`
		if got := string(doc["withheld"]); got != want {
			t.Errorf("withheld = %s, want %s", got, want)
		}
		if got := string(doc["notes"]); got != `"short"` {
			t.Errorf("notes within the budget changed: %s", got)
		}
	})
}

func TestShowHeadGetRequest(t *testing.T) {
	t.Run("always reads the comments and brief dependencies", func(t *testing.T) {
		got := showHeadGetRequest("be-abc", false)
		if got.ID != "be-abc" || !got.IncludeComments || !got.BriefDeps || got.IncludeDependents {
			t.Errorf("showHeadGetRequest = %+v", got)
		}
		if got := showHeadGetRequest("be-abc", true); !got.IncludeDependents {
			t.Errorf("showHeadGetRequest dropped IncludeDependents: %+v", got)
		}
	})

	t.Run("the proxied route builds the same request under --head only", func(t *testing.T) {
		in := &showProxiedInput{head: true, includeDepends: true}
		if got, want := in.getRequest("be-abc"), showHeadGetRequest("be-abc", true); got != want {
			t.Errorf("proxied --head request = %+v, want %+v", got, want)
		}
		if got := (&showProxiedInput{}).getRequest("be-abc"); got.IncludeComments || got.BriefDeps {
			t.Errorf("without --head the proxied request reads what the head view reads: %+v", got)
		}
	})
}
