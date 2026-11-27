package main

import (
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

// bd show --head is a bounded read of one issue (GH#7187): the header, the
// labels and dependencies as bd show prints them, the END of the notes field,
// and the newest comments, closed by one line that names everything the view
// left out. It exists because a long-lived issue grows to hundreds of KB and
// bd show prints all of it, so a reader who pays for every byte either skips
// the issue or reads a stale part of it.
//
// It is a text view only and it stores nothing: no field is added, moved or
// rewritten, and bd show without the flag prints what it printed before.

const (
	// headNotesBytes is the notes budget of the head view.
	headNotesBytes = 1024
	// headCommentsTail is how many of the newest comments the head view
	// renders when --comments-tail is not given.
	headCommentsTail = 3
	// headLineSlack is the most of the notes budget the view gives up to
	// open on a line start; see clipNotesTail.
	headLineSlack = headNotesBytes / 4
)

// showHeadConflict names the flag --head cannot be combined with, or returns
// "" when there is none. --head is one view of an issue, and each of these
// flags selects another view or another output shape, so a combination has no
// single meaning; refusing it is better than letting one flag win silently,
// because a reader who asked for a bounded view and got the full record has
// paid for the bytes before noticing.
func showHeadConflict(cmd *cobra.Command, jsonOut bool) string {
	if jsonOut {
		return "json"
	}
	for _, name := range []string{"short", "long", "refs", "children", "thread", "watch"} {
		if on, _ := cmd.Flags().GetBool(name); on {
			return name
		}
	}
	if ref, _ := cmd.Flags().GetString("as-of"); ref != "" {
		return "as-of"
	}
	return ""
}

// validateShowHead is the usage error both show routes (direct and
// --proxied-server) return for a --head combination showHeadConflict names.
func validateShowHead(cmd *cobra.Command, jsonOut bool) error {
	switch name := showHeadConflict(cmd, jsonOut); name {
	case "":
		return nil
	case "json":
		// JSON output can come from BD_JSON or a config file as well as the
		// flag, so the message names the way out for a caller who never
		// typed --json.
		return HandleErrorRespectJSON("--head is a text view and cannot be combined with --json (pass --json=false when JSON output is set in the environment or the config file); in JSON, --brief-deps bounds the dependencies and comment bodies are left out unless --include-comments is given")
	default:
		return HandleErrorRespectJSON("--head cannot be combined with --%s", name)
	}
}

// resolveHeadCommentsTail is the comments cap of the head view: an explicit
// --comments-tail wins (0 included, which renders every comment), and
// otherwise the view renders the newest headCommentsTail.
func resolveHeadCommentsTail(cmd *cobra.Command, commentsTail int) int {
	if cmd.Flags().Changed("comments-tail") {
		return commentsTail
	}
	return headCommentsTail
}

// clipNotesTail returns the end of notes that fits in budget bytes, and how
// many bytes before it were withheld (0 when notes already fits).
//
// It keeps the END because notes are appended to (bd update --append-notes),
// so the newest state is last; keeping the start would show the stalest text.
// The cut never splits a UTF-8 sequence. When it lands inside a line and the
// next line starts within slack bytes, the cut moves forward to that line
// start, so the view does not open on half a sentence. A longer partial line
// is kept from the cut: giving up most of the budget to reach a line start
// would show a long paragraph followed by a short line as the short line
// alone.
func clipNotesTail(notes string, budget, slack int) (tail string, withheld int) {
	if budget <= 0 || len(notes) <= budget {
		return notes, 0
	}
	start := len(notes) - budget
	for start < len(notes) && !utf8.RuneStart(notes[start]) {
		start++
	}
	if notes[start-1] != '\n' {
		if i := strings.IndexByte(notes[start:], '\n'); i >= 0 && i < slack {
			if next := start + i + 1; strings.TrimSpace(notes[next:]) != "" {
				start = next
			}
		}
	}
	return notes[start:], start
}

// printHeadNotes prints the NOTES section of the head view: the heading, one
// muted line naming the bytes withheld when the field was clipped, then the
// kept end rendered as bd show renders notes. Empty notes print nothing, as
// in the full view.
func printHeadNotes(notes string) {
	if notes == "" {
		return
	}
	tail, withheld := clipNotesTail(notes, headNotesBytes, headLineSlack)
	fmt.Printf("\n%s\n", ui.RenderBold("NOTES"))
	if withheld > 0 {
		fmt.Printf("  %s\n", ui.RenderMuted(fmt.Sprintf("… %s of earlier notes withheld", formatBytes(int64(withheld)))))
	}
	fmt.Printf("%s\n", uimd.RenderMarkdown(tail))
}

// headWithheld lists what the head view left out of this issue, in the order
// bd show prints the fields. tailN is the comments cap the view rendered with
// (see printComments for when a cap hides anything). commentsUnread says the
// comments could not be read: both routes render an issue without them
// rather than not at all, and a view that then said nothing was withheld
// would state the opposite of what happened.
func headWithheld(issue *types.Issue, commentCount, tailN int, commentsUnread bool) []string {
	var parts []string
	field := func(name, value string) {
		if value != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", name, formatBytes(int64(len(value)))))
		}
	}
	field("description", issue.Description)
	field("design", issue.Design)
	if _, withheld := clipNotesTail(issue.Notes, headNotesBytes, headLineSlack); withheld > 0 {
		parts = append(parts, fmt.Sprintf("earlier notes (%s)", formatBytes(int64(withheld))))
	}
	field("acceptance criteria", issue.AcceptanceCriteria)
	if tailN > 0 && tailN < commentCount {
		hidden := commentCount - tailN
		noun := "comments"
		if hidden == 1 {
			noun = "comment"
		}
		parts = append(parts, fmt.Sprintf("%d older %s", hidden, noun))
	}
	if commentsUnread {
		parts = append(parts, "the comments (they could not be read)")
	}
	return parts
}

// printHeadTrailer prints the closing line of the head view. It is printed
// whether or not anything was withheld, so a reader can tell a short issue
// from a clipped one without a second read.
func printHeadTrailer(issue *types.Issue, commentCount, tailN int, commentsUnread bool) {
	parts := headWithheld(issue, commentCount, tailN, commentsUnread)
	if len(parts) == 0 {
		fmt.Printf("\n  %s\n", ui.RenderMuted("… head view: nothing withheld"))
		return
	}
	// issue.ID is read from the store, so it goes into the command as one
	// shell word or as a placeholder named after it (remedy.CommandArg), the
	// same way printComments points at the full record.
	arg, note := remedy.CommandArg(runtime.GOOS, issue.ID, "<issue-id>")
	fmt.Printf("\n  %s\n", ui.RenderMuted(fmt.Sprintf("… head view: withheld %s — bd show %s for the full record%s", strings.Join(parts, ", "), arg, note)))
}
