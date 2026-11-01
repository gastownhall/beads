package issueops

import (
	"context"
	"fmt"
)

// PromoteRequest describes one `bd promote`: move a wisp onto the durable
// issues plane and record why.
type PromoteRequest struct {
	// Actor is the principal the move and the comment are attributed to. It
	// must not be empty.
	Actor string
	// IssueID is the exact canonical id of the wisp. It must not be empty;
	// fuzzy or prefix resolution is the caller's, for the reason
	// GetRequest.ID gives.
	IssueID string
	// Reason is the optional text appended to the promotion comment.
	Reason string
	// Provenance labels the version-control history entry of the move, as
	// UpdateRequest.Provenance describes. A remote backend labels its own
	// entry, so a caller writing through one leaves it empty.
	Provenance string
}

// PromoteResult reports a promotion.
type PromoteResult struct {
	// Issue is the promoted row as the move left it.
	Issue *Issue
	// CommentErr is set when the move succeeded but the promotion comment
	// could not be written. The promotion stands: the comment is the audit
	// note, not the act, and bd promote has always reported a failed comment
	// as a warning rather than undoing the move.
	CommentErr error
}

// PromoteRoles are the three roles Promote composes. Every backend serves
// all three, so a promotion reads and writes the same way on every route.
type PromoteRoles struct {
	Reader    Reader
	Lifecycle Lifecycle
	Commenter Commenter
}

// NotAWispError refuses a promotion of a row that is not an ephemeral wisp:
// it is already durable, or another writer promoted it first. A no-history
// wisp (NoHistory set, Ephemeral not) is refused with the same sentence,
// because the gate reads the Ephemeral flag as `bd promote` always has;
// `bd update --persistent` is the move for that row.
type NotAWispError struct {
	ID string
}

func (e *NotAWispError) Error() string {
	return fmt.Sprintf("%s is not a wisp (already persistent)", e.ID)
}

// PromotionComment is the audit comment a promotion records on the promoted
// row. Every route records exactly this text.
func PromotionComment(reason string) string {
	comment := "Promoted from Level 0"
	if reason != "" {
		comment += ": " + reason
	}
	return comment
}

// Promote is the one library entry for `bd promote`. It reads the row,
// refuses one that is not an ephemeral wisp (*NotAWispError), moves it to the
// durable plane with ONE Lifecycle.Update whose patch is the persistence move
// alone (Persistence = persistent: the row keeps its id, and its labels,
// dependencies, events and comments move with it, atomically, in the role's
// own transaction), and then records the promotion comment through the
// Commenter.
//
// A move that changed nothing lost a race to another promoter, and is
// reported as *NotAWispError without a second comment. Every refusal of the
// read or the move is returned unchanged, so a backend that cannot express
// the move (an http server without the issues.update.persistence capability)
// reaches the caller as its own typed refusal, before anything was written.
func Promote(ctx context.Context, roles PromoteRoles, req PromoteRequest) (PromoteResult, error) {
	if req.Actor == "" {
		return PromoteResult{}, fmt.Errorf("%w: promote: actor is required", ErrValidation)
	}
	if req.IssueID == "" {
		return PromoteResult{}, fmt.Errorf("%w: promote: issue id is required", ErrValidation)
	}
	current, err := roles.Reader.Get(ctx, GetRequest{ID: req.IssueID})
	if err != nil {
		return PromoteResult{}, err
	}
	if !current.Ephemeral {
		return PromoteResult{}, &NotAWispError{ID: req.IssueID}
	}
	moved, err := roles.Lifecycle.Update(ctx, UpdateRequest{
		Actor:   req.Actor,
		IssueID: req.IssueID,
		Patch: IssuePatch{
			Persistence: Field[PersistenceMode]{Set: true, Value: PersistenceModePersistent},
		},
		Provenance: req.Provenance,
	})
	if err != nil {
		return PromoteResult{}, err
	}
	if !moved.Changed {
		return PromoteResult{}, &NotAWispError{ID: req.IssueID}
	}
	result := PromoteResult{Issue: moved.Issue}
	if _, err := roles.Commenter.AddComment(ctx, AddCommentRequest{
		Author:  req.Actor,
		IssueID: req.IssueID,
		Text:    PromotionComment(req.Reason),
	}); err != nil {
		result.CommentErr = err
	}
	return result, nil
}
