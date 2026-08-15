package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateAddCommentRequest applies the request rules every Commenter
// implementation shares.
//
// Blankness is decided on a TRIMMED copy and the request's own Text is left
// alone: a comment of nothing but whitespace carries no information and is
// almost always a shell quoting accident, but a comment that merely begins
// with a newline is a comment.
func ValidateAddCommentRequest(request publicops.AddCommentRequest) error {
	if request.Author == "" {
		return fmt.Errorf("%w: add comment requires an author", storage.ErrValidation)
	}
	if request.IssueID == "" {
		return fmt.Errorf("%w: add comment requires an issue ID", storage.ErrValidation)
	}
	if strings.TrimSpace(request.Text) == "" {
		return fmt.Errorf("%w: comment text cannot be empty", storage.ErrValidation)
	}
	return nil
}

// AddCommentCommitMessage is the history entry a comment records. It is the
// spelling both stores' own AddIssueComment already wrote.
func AddCommentCommitMessage(issueID string) string {
	return "bd: comment " + issueID
}

// ExecuteAddComment appends one comment in tx and reports the durable tables
// changed. It is the store-backed body behind the Commenter accessor; the
// unit-of-work provider has its own, for the reason Lifecycle does.
//
// A comment on an ephemeral row changes only wisp_comments, which
// ChangedTables drops, so the caller's transaction commits nothing and records
// no history entry: the wisp tables are dolt-ignored and there is nothing to
// version.
func ExecuteAddComment(ctx context.Context, tx *sql.Tx, request publicops.AddCommentRequest) (publicops.AddCommentResult, ChangedTables, error) {
	commentTable, err := resolveCommentPlaneInTx(ctx, tx, request.IssueID)
	if err != nil {
		return publicops.AddCommentResult{}, nil, err
	}
	comment, err := AddIssueCommentInTx(ctx, tx, request.IssueID, request.Author, request.Text)
	if err != nil {
		return publicops.AddCommentResult{}, nil, err
	}
	tables := ChangedTables{}
	tables.Add(commentTable)
	return publicops.AddCommentResult{Comment: comment}, tables, nil
}

// resolveCommentPlaneInTx names the comment table the anchor's thread lives in,
// refusing an id that names neither an issue nor a wisp.
//
// The existence probe is here rather than left to the insert's own so the
// refusal is TYPED: AddIssueCommentInTx reports a missing anchor as prose, and
// a caller of this role classifies with errors.Is. It resolves the plane in
// the same transaction the insert runs in, so a comment cannot land on a row
// an earlier read saw and this one did not.
//
//nolint:gosec // G201: issueTable comes from WispTableRouting ("issues" or "wisps")
func resolveCommentPlaneInTx(ctx context.Context, tx *sql.Tx, issueID string) (string, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	var exists bool
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, issueTable), issueID).Scan(&exists); err != nil {
		return "", fmt.Errorf("check issue existence: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("%w: issue %s", storage.ErrNotFound, issueID)
	}
	if isWisp {
		return "wisp_comments", nil
	}
	return "comments", nil
}
