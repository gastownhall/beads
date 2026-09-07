package workapi

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// CommentStreamer is the one method list-comment hydration needs off a detail
// source. It is named separately, and taken as the narrower interface, so the
// hydration body cannot quietly grow a second read: DetailSource satisfies it,
// and both of the sources that exist (NewStoreDetailSource, NewUOWDetailSource)
// are therefore usable here without a new adapter.
type CommentStreamer interface {
	IterComments(ctx context.Context, id string, isWisp bool) (storage.Iter[types.Comment], error)
}

// HydrateListComments fills in the comment half of one list page.
//
// It is the shared epilogue step FinishPageAt is, and for the identical
// reason: `bd list --json` reaches this through two Reader.List
// implementations — the store-backed one and the unit-of-work one — and a
// hydration written out longhand in each is how the two came apart before.
// One body, called from both, is what makes a CLI listing and an HTTP one
// answer the same request the same way.
//
// TWO OUTCOMES, AND NEITHER IS SILENT.
//
//	include = true   every row's Issue.Comments is populated, and any row that
//	                 cannot be read fails the whole call. A caller that asked
//	                 for the bodies gets them or gets an error; it never gets a
//	                 short list it has no way to detect.
//	include = false  every row with a nonzero CommentCount is marked
//	                 CommentsOmitted, so an absent `comments` key means "none"
//	                 on one row and "not asked for" on the other, and the row
//	                 says which.
//
// THE SOURCE IS A CONSTRUCTOR, NOT A SOURCE, AND THAT IS LOAD-BEARING RATHER
// THAN STYLE. Comment hydration is opt-in, so the DEPENDENCY on a comment
// reader must be opt-in with it: building one eagerly on every listing makes a
// capability that only the opt-in path uses into a requirement every caller
// has to satisfy. It is not hypothetical — the unit-of-work source is built
// from four use-case accessors, and constructing it unconditionally turned
// every `GET /v0/beads/issues` on a provider without a comment use case into a
// panic, on a request that had asked for no comments at all. A count-only
// listing now calls nothing.
//
// THE MARKER IS THE POINT, not the hydration (be-73x). A page that silently
// carries no comment text answers a content search with a plausible non-zero
// result whose matching rows are missing, and nothing in that answer invites a
// second look. The count alone does not fix it: comment_count sits on the row
// accurately reporting how much is not there, which reads as reassurance
// rather than as a warning.
//
// A ZERO COUNT IS NOT ALWAYS ZERO COMMENTS, AND countsKnown IS WHAT SEPARATES
// THE TWO. Under issueops.ListRequest.SkipCounts the cardinalities come back
// zero meaning UNKNOWN — the request type says so in as many words — so a body
// that reads CommentCount as authoritative would answer IncludeComments with
// an empty page and no error. countsKnown false therefore means: query every
// row, because there is no count to prove a row has nothing to fetch, and mark
// no row omitted, because the marker asserts a nonzero count this page cannot
// support.
//
// It is a PARAMETER rather than an assumption because issueops.ListRequest is
// PUBLIC and permits both fields at once. An earlier version of this body
// skipped every row on that combination and justified it in a comment reading
// "the JSON route never sets SkipCounts" — true of today's CLI callers, and
// not a property of the contract they were reading. The HTTP surface and any
// future caller may set both.
func HydrateListComments(ctx context.Context, newSrc func() CommentStreamer, items []*types.IssueWithCounts, include, countsKnown bool) error {
	if len(items) == 0 {
		return nil
	}
	if !include {
		if countsKnown {
			markCommentsOmitted(items)
		}
		return nil
	}
	if newSrc == nil {
		return fmt.Errorf("hydrate list comments: comment source must not be nil")
	}
	src := newSrc()
	if src == nil {
		return fmt.Errorf("hydrate list comments: comment source must not be nil")
	}
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		// A row the page can PROVE has no comments needs no query. Only a
		// hydrated count proves it: under SkipCounts a zero means unknown, and
		// treating it as none is how this body once dropped every comment a
		// caller had asked for.
		if countsKnown && item.CommentCount == 0 {
			continue
		}
		comments, err := collectComments(ctx, src, item.ID, isWispPlane(item.Issue))
		if err != nil {
			return fmt.Errorf("hydrate list comments: %w", err)
		}
		item.Issue.Comments = comments
	}
	return nil
}

// markCommentsOmitted flags the rows whose comment text was never fetched.
// Rows the caller already hydrated are left alone: CommentsOmitted never
// appears beside a populated slice, which is what lets a consumer read the two
// fields as one unambiguous answer.
//
// It is called only when the counts are real. The marker's meaning is "this
// row HAS comments and they are not here", so a page whose counts were skipped
// cannot honestly set it on any row — it does not know. That page is less
// informative, which is what its caller asked for by skipping the counts.
func markCommentsOmitted(items []*types.IssueWithCounts) {
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		if item.CommentCount > 0 && item.Issue.Comments == nil {
			omitted := true
			item.CommentsOmitted = &omitted
		}
	}
}

// isWispPlane reports which storage plane a row's comments live in.
//
// It is internal/storage/issueops.IsWisp's rule, restated rather than called
// because this package does not import that one. Both halves matter: the
// override pins the plane for an in-memory record and the two flags are the
// inference everywhere else. The store-backed detail source ignores the answer
// — it routes ids to the right table itself — and the unit-of-work one needs
// it, so getting it wrong is a wrong-table read on exactly one of the two
// seams, which is the kind of divergence this package exists to prevent.
func isWispPlane(issue *types.Issue) bool {
	if issue.WispPlaneOverride != nil {
		return *issue.WispPlaneOverride
	}
	return issue.Ephemeral || issue.NoHistory
}
