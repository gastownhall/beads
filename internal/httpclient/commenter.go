// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/commenter.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// httpCommenter is issueops.Commenter over addComment (POST
// /v0/beads/issues/{id}/comments) — the TWENTY-FOURTH wire-backed accessor, and
// the only write on this surface whose path names a SUB-RESOURCE.
//
// THE MAPPING IS TOTAL and unusually small: three request members, of which one
// is the path, and one result member. There is no ledger row for this operation
// and no member refuses.
//
// THE AUTHOR IS CALLER-ASSERTED, and it is spelled `author` rather than `actor`
// because it is not the same thing. Every other write here names the principal a
// mutation is ATTRIBUTED to; this value is part of what the write STORED — it
// lands in the row and is read back by everyone who sees the thread. It is also
// not the authenticated principal even where a bearer is required, because the
// token a deployment configures is shared and admits a client to the whole
// surface. So THIS CLIENT sends it verbatim: no default, no fallback to a
// configured actor, no normalization.
//
// THE SERVER'S RULE IS A DIFFERENT MATTER, and the difference is ledgered rather
// than described away (L-comment-author). The operation applies the `actor`
// rules to this member — trim, then refuse a control rune — and passes the
// TRIMMED value on, while the role itself only checks the field is non-empty and
// stores what it was given. So a padded author is stored trimmed here and
// verbatim on a local leg, and one carrying a C1 introducer is refused here and
// stored there. Anticipating either half on this side would be worse than the
// divergence: trimming would silently rewrite a caller's request, and refusing
// would be a second copy of a server rule that is free to move. The value goes
// as it was written and the server's answer comes back unchanged, which is what
// keeps the difference visible.
//
// THE TEXT IS SHAPE-ONLY, and that is the column rather than an omission. It is
// a LONGTEXT the caller may fill with a stack trace or a diff, so there is no
// length bound to apply and no control rule — a comment is written in newlines.
// The only cap is the 1 MiB every body on this surface shares, and BOTH PLANES
// agree about that number only because migration 0065 widened
// `wisp_comments.text` from TEXT's 65535 bytes to match the durable column. That
// is worth a sentence here rather than only upstream: this operation resolves
// its anchor across both planes, so before 0065 the bound a caller was subject
// to depended on which plane its id happened to name, and nothing above the
// storage seam could have told it which.
//
// A COMMENT ON A WISP LANDS AND LEAVES NO DURABLE TRACE. The wisp tables are
// dolt-ignored, so an ephemeral thread records no history entry — none, not one
// — while the comment itself is stored and reads back. Nothing here decides
// that: the role resolves the plane inside the transaction it writes in, which
// is what stops a comment landing on a row an earlier read saw.
//
// THERE IS NO READ HERE, and its absence is the role's ruling rather than a
// wave's backlog. A comment page is a paging question with a cursor of its own,
// the wire publishes no such operation, and the thread is read through
// `GET /v0/beads/issues/{id}?include_comments=true`. A client that invented one
// — a filtered getIssue, a fan-out — would be answering a question no role asked
// and no server serves.
type httpCommenter struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Commenter = (*httpCommenter)(nil)

// AddComment appends one comment and answers the row as stored.
func (c *httpCommenter) AddComment(ctx context.Context, req issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	// The role's own request rules, taken from the one place every Commenter
	// shares them rather than restated here — readyclaimer.go's lesson, applied
	// before it can be learned twice. All three refusals are ErrValidation and
	// all three are raised BEFORE the dial: an empty author and an empty id would
	// otherwise cost a round trip to be told what the contract already says, and
	// a blank body would be refused by the server with the role's own sentence
	// anyway, which is the same sentence this raises.
	if err := storageops.ValidateAddCommentRequest(req); err != nil {
		return issueops.AddCommentResult{}, err
	}

	comment, err := c.wire.AddComment(ctx, req.IssueID, apigen.AddCommentRequest{
		Author: req.Author,
		Text:   req.Text,
	})
	if err != nil {
		return issueops.AddCommentResult{}, err
	}
	// PRESENCE IS CHECKED RATHER THAN TRUSTED, for decodeCountGroups' reason and
	// with a sharper consequence: the result's whole payload is this pointer, and
	// the role promises the stored id and the stored created_at — the value a
	// caller may use directly as a comment-page cursor. A nil here would be a
	// server that answered 200 with no body at all, and returning it would move
	// the panic to whichever caller dereferenced it first.
	if comment == nil {
		return issueops.AddCommentResult{}, fmt.Errorf(
			"bd serve answered an appended comment with no body: the operation returns the stored row, " +
				"and an absent one carries neither the id the insert minted nor the created_at a caller pages from")
	}
	// A COPY, so nothing downstream holds a window onto a decoded response body.
	// apigen.Comment IS types.Comment — a Go type alias the document pins — so
	// this is an assignment rather than a conversion, and there is no second wire
	// struct to fall out of step.
	stored := *comment
	return issueops.AddCommentResult{Comment: &stored}, nil
}
