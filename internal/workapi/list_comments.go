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
// A ZERO COUNT IS NOT ALWAYS ZERO COMMENTS, which is why the marker is gated
// on CommentCount rather than written unconditionally. Under
// issueops.ListRequest.SkipCounts the cardinalities come back zero meaning
// UNKNOWN, and a marker derived from them would be absent on every row of a
// page that hydrated nothing. That combination does not reach a JSON listing —
// the text renderings are the only SkipCounts callers and they ask for no
// comments — but the rule is stated here rather than left to hold by accident.
func HydrateListComments(ctx context.Context, newSrc func() CommentStreamer, items []*types.IssueWithCounts, include bool) error {
	if len(items) == 0 {
		return nil
	}
	if !include {
		markCommentsOmitted(items)
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
		// A row with no comments needs no query. The count is authoritative
		// here because the JSON route never sets SkipCounts; see the doc
		// comment for why the skipped-count case is spelled out rather than
		// assumed away.
		if item.CommentCount == 0 {
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
