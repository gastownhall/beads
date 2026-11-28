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
	"github.com/steveyegge/beads/issueops"
)

// bd show --head is a bounded read of one issue (GH#7187): the header, the
// labels and dependencies as bd show prints them, the END of the notes field,
// and the newest comments, closed by one line that names everything the view
// left out. It exists because a long-lived issue grows to hundreds of KB and
// bd show prints all of it, so a reader who pays for every byte either skips
// the issue or reads a stale part of it.
//
// It stores nothing: no field is added, moved or rewritten, and bd show
// without the flag prints what it printed before.
//
// With --json the same subset is one JSON object per issue (headDetails): the
// read goes through the reader role's Get like every bd show --json, and this
// file projects the answer. The read contract itself (issueops.GetRequest and
// the HTTP operation behind it) has no head parameter, so the projection
// reads the same over every backend the contract already serves.

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
func showHeadConflict(cmd *cobra.Command) string {
	for _, name := range []string{"short", "long", "refs", "children", "thread", "watch", "include-comments"} {
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
func validateShowHead(cmd *cobra.Command) error {
	switch name := showHeadConflict(cmd); name {
	case "":
		return nil
	case "include-comments":
		// --include-comments asks for every comment body and the head view
		// carries the newest few, so the message names the flag that sets
		// how many instead of leaving the caller with three of fifty.
		return HandleErrorRespectJSON("--head cannot be combined with --include-comments: the head view carries the newest comments, and --comments-tail N sets how many (0 for all of them)")
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

// headWithheldCounts is what the head view left out of one issue, as numbers.
// The text view words it as its closing line (headWithheld) and the JSON view
// carries it as the withheld object, so the two cannot count differently.
//
// Every field is always encoded, zero included: a reader tells a head view
// from a full record by the presence of the object, and a short issue from a
// clipped one by its zeros, without a second read.
type headWithheldCounts struct {
	// DescriptionBytes, DesignBytes and AcceptanceCriteriaBytes are the whole
	// size of each field, because the head view leaves those fields out.
	DescriptionBytes int `json:"description_bytes"`
	DesignBytes      int `json:"design_bytes"`
	// NotesBytes is the size of the earlier notes that clipNotesTail cut
	// off, not the size of the field: the end of the notes is in the view.
	NotesBytes              int `json:"notes_bytes"`
	AcceptanceCriteriaBytes int `json:"acceptance_criteria_bytes"`
	// Comments is how many older comments the comments cap left out.
	Comments int `json:"comments"`
}

// countHeadWithheld measures what the head view leaves out of issue. tailN is
// the comments cap the view was built with: 0 keeps every comment, and so
// does a cap no smaller than commentCount (see printComments).
func countHeadWithheld(issue *types.Issue, commentCount, tailN int) headWithheldCounts {
	w := headWithheldCounts{
		DescriptionBytes:        len(issue.Description),
		DesignBytes:             len(issue.Design),
		AcceptanceCriteriaBytes: len(issue.AcceptanceCriteria),
	}
	_, w.NotesBytes = clipNotesTail(issue.Notes, headNotesBytes, headLineSlack)
	if tailN > 0 && tailN < commentCount {
		w.Comments = commentCount - tailN
	}
	return w
}

// headWithheld lists what the head view left out of this issue, in the order
// bd show prints the fields. tailN is the comments cap the view rendered with
// (see printComments for when a cap hides anything). commentsUnread says the
// comments could not be read: both routes render an issue without them
// rather than not at all, and a view that then said nothing was withheld
// would state the opposite of what happened.
func headWithheld(issue *types.Issue, commentCount, tailN int, commentsUnread bool) []string {
	w := countHeadWithheld(issue, commentCount, tailN)
	var parts []string
	field := func(name string, size int) {
		if size > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", name, formatBytes(int64(size))))
		}
	}
	field("description", w.DescriptionBytes)
	field("design", w.DesignBytes)
	field("earlier notes", w.NotesBytes)
	field("acceptance criteria", w.AcceptanceCriteriaBytes)
	if w.Comments > 0 {
		noun := "comments"
		if w.Comments == 1 {
			noun = "comment"
		}
		parts = append(parts, fmt.Sprintf("%d older %s", w.Comments, noun))
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

// headDetails is one issue of bd show --head --json: the detail view the
// reader role answered with, cut down to the head view, and a withheld object
// that counts what the cut left out.
//
// It embeds the answer instead of naming its fields, so every key keeps the
// name and the meaning it has in bd show --json and a field the contract
// gains later appears here too. Four things differ, and withheld counts each:
// description, design and acceptance_criteria are absent, notes is the end of
// the field (clipNotesTail), and comments holds the newest ones only.
// comment_count is still the count of all of them.
type headDetails struct {
	*types.IssueDetails
	Withheld headWithheldCounts `json:"withheld"`
}

// showHeadGetRequest is the read the JSON head view makes. It always asks for
// the comment bodies, because the view carries the newest ones and a plain
// bd show --json leaves them out; newHeadDetails drops the older ones. It
// always asks for brief dependencies, because a full dependency row carries
// that issue's own description, design and notes, which would put back the
// bulk the view exists to leave out. Dependents stay behind
// --include-dependents, as in every bd show --json.
//
// The view bounds what is printed, not what is read: the reader streams every
// comment body and all but the newest are dropped here, as the text view has
// always read every comment to print three. A read bounded at the store would
// need a tail parameter on the read contract, which this view does not add.
func showHeadGetRequest(id string, includeDependents bool) issueops.GetRequest {
	return issueops.GetRequest{
		ID:                id,
		IncludeDependents: includeDependents,
		IncludeComments:   true,
		BriefDeps:         true,
	}
}

// newHeadDetails cuts a detail view down to the head view. tailN is the
// comments cap (resolveHeadCommentsTail). It works on a copy and re-slices
// the comments instead of rewriting them, because the reader that answered
// owns details and may hand the same rows to another caller.
func newHeadDetails(details *types.IssueDetails, tailN int) *headDetails {
	if details == nil {
		// A reader that answers with no details and no error encodes as
		// null without --head, and so it does here.
		return nil
	}
	head := *details
	withheld := countHeadWithheld(&details.Issue, len(details.Comments), tailN)
	head.Description = ""
	head.Design = ""
	head.AcceptanceCriteria = ""
	head.Notes, _ = clipNotesTail(details.Notes, headNotesBytes, headLineSlack)
	head.Comments = details.Comments[withheld.Comments:]
	return &headDetails{IssueDetails: &head, Withheld: withheld}
}
