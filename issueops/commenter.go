package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// Comment is one comment on an issue.
type Comment = types.Comment

// AddCommentRequest describes one comment.
type AddCommentRequest struct {
	// Author is the commenter and must not be empty. It is spelled Author
	// rather than Actor because a comment is signed: the name lands in the row
	// and is read back by everyone who sees the thread, where an Actor is the
	// principal a mutation is attributed to. The other requests here mutate an
	// issue on someone's behalf; this one publishes under their name.
	Author string
	// IssueID is the exact canonical id and must not be empty. The
	// issue-to-wisp fallback happens INSIDE, exactly as it does for Reader.Get,
	// so a caller never has to know which plane the thread lives on. There is
	// no fuzzy or prefix resolution here, for the reason GetRequest.ID gives.
	//
	// Nor is there a plane ORDER to know, for the reason GetRequest.ID also
	// gives: no local write path makes an id resident in both planes, so a
	// comment can only land on the one thread that exists. GetRequest.ID owns
	// that argument for every role that resolves an id; this one inherits it
	// whole, INCLUDING ITS TWO CAVEATS — in particular that replication can
	// produce a dual-resident id that no local guard closes, which is a corrupt
	// store rather than a question this order would answer.
	IssueID string
	// Text is the comment body and must not be blank. It is stored verbatim:
	// blankness is decided on a trimmed copy, and nothing trims the value that
	// lands in the row.
	Text string
}

// AddCommentResult reports the stored comment.
type AddCommentResult struct {
	// Comment is the stored comment, with the id and created_at the row
	// actually got. CreatedAt is the STORED value at the column's precision,
	// not the wall clock the call happened at, so it is safe to use directly
	// as a comment-page cursor.
	Comment *Comment
}

// Commenter describes adding a comment to an issue: the write side of `bd
// comment`, and — like Lifecycle, Reader, ReadyClaimer, BatchCloser and
// DependencyEditor — a role with its own accessor. A new capability gets a new
// role interface and its own accessor; never append a method here.
//
// It is its own role rather than a Lifecycle verb because a comment is not a
// patch to an issue. It appends a row to a thread the issue owns and leaves
// every field of the issue untouched, so an IssuePatch has nothing to carry
// and an UpdateResult has nowhere to put the comment.
//
// READING the thread is deliberately not here. Reader.Get already returns
// comments when asked, and a comment-page walk is a paging question with a
// cursor of its own — a second question, so a second role when something needs
// it, not a second method here.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. Deterministic request-validation failures match
// ErrValidation and leave persistent state unchanged.
type Commenter interface {
	// AddComment appends one comment as ONE atomic mutation, with at most one
	// history entry. A blank Text is ErrValidation, and so is an empty
	// IssueID; a NON-EMPTY id that names neither an issue nor a wisp is
	// ErrNotFound. Refusals use the same typed vocabulary Lifecycle returns,
	// so a caller classifies them with errors.Is rather than by reading prose.
	//
	// A comment on an EPHEMERAL row records NO durable history entry — none,
	// not "at most one". Ephemeral rows are not versioned, and the wisp tables
	// are dolt-ignored precisely so ephemeral work never ships, so a history
	// entry naming a wisp thread would be the sync artifact ignoring those
	// tables exists to prevent. The comment itself lands on the ephemeral
	// thread and reads back from it; only the durable trace is absent, so a
	// caller reconstructing threads from durable history alone will not see
	// it.
	AddComment(ctx context.Context, req AddCommentRequest) (AddCommentResult, error)
}
