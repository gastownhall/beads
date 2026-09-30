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

// CommentPlanes answers, for one page of ids, which of them keep their comments
// on the WISP plane: an id the map reports true for is read from wisp_comments,
// and an id it reports false for or omits from comments.
//
// IT IS A QUERY, NOT A PREDICATE ON THE ROW, because residence is the answer
// and only a query has it. An earlier version of this body derived the plane
// from the row's own Ephemeral/NoHistory flags — issueops.IsWisp's rule, which
// is the right rule for routing a record being WRITTEN — and that inference is
// wrong on a read for a row class the tree deliberately maintains. `bd import`
// pins a `no_history` record to the durable table while PRESERVING the flag on
// the row (cmd/bd/import_shared.go, applyImportWispPlane: clearing it would
// change the content hash and break export→import→export byte-identity, so
// "only the routing is pinned"), and every clone materializes its database by
// importing JSONL. A promoted no-history bead that has been through
// export→import is therefore a durable row whose flags say wisp, and reading
// its comments by those flags queries wisp_comments, finds nothing, and answers
// with a nonzero comment_count, an empty comments array, no marker and no error
// — be-73x's own failure mode, under the flag whose contract is "gets them or
// gets an error".
//
// Every other comment-touching role already resolves residence instead of
// inferring it: GetIssueOrWisp behind both Reader.Get implementations and behind
// uow.commenter.AddComment, DoltStore.GetIssueComments through isActiveWisp,
// issueops.GetCommentCountsInTx through PartitionWispIDsInTx. So `bd comment`
// writes such a row's comments where `bd show` reads them, and a listing that
// went by the flags was the one role that disagreed with the other three about
// the same row. A page is that same question asked about many ids, so it is
// answered the same way, once per page rather than once per row.
type CommentPlanes func(ctx context.Context, ids []string) (map[string]bool, error)

// SourceRoutesCommentPlanes is the CommentPlanes to pass when the comment
// source needs no plane told to it because it resolves each id itself: the
// store-backed source is the one that does (DoltStore.GetIssueComments picks
// the table from isActiveWisp), and it ignores the argument entirely.
//
// It is a NAMED no-op rather than a nil default so that the seam with a routing
// question and the seam without one are told apart at the call site, and so a
// third seam added later has to answer the question rather than inherit
// "durable" by omission — which is exactly how the wrong-table read described
// on CommentPlanes got in.
func SourceRoutesCommentPlanes(context.Context, []string) (map[string]bool, error) {
	return nil, nil
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
//
// THE PLANE COMES FROM THE PAGE'S SEAM, NOT FROM THE ROW. Which comment table
// an id's comments live in is a fact about where the row was found, so the
// caller resolves it (CommentPlanes) rather than this body inferring it from
// the row's flags — see CommentPlanes for the row class where the flags and the
// table disagree, and why that made a listing answer with content-free rows.
func HydrateListComments(ctx context.Context, newSrc func() CommentStreamer, planes CommentPlanes, items []*types.IssueWithCounts, include, countsKnown bool) error {
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
	if planes == nil {
		return fmt.Errorf("hydrate list comments: comment plane resolution must not be nil (pass SourceRoutesCommentPlanes when the source routes ids itself)")
	}
	rows := rowsNeedingComments(items, countsKnown)
	if len(rows) == 0 {
		return nil
	}
	src := newSrc()
	if src == nil {
		return fmt.Errorf("hydrate list comments: comment source must not be nil")
	}
	// One plane query for the whole page, and only for the rows that are about
	// to be read: the ids are the same set the loop below walks, so a page that
	// needs no comment read pays for no plane lookup either.
	wisp, err := planes(ctx, rowIDs(rows))
	if err != nil {
		return fmt.Errorf("hydrate list comments: resolve comment planes: %w", err)
	}
	for _, item := range rows {
		comments, err := collectComments(ctx, src, item.ID, wisp[item.ID])
		if err != nil {
			return fmt.Errorf("hydrate list comments: %w", err)
		}
		item.Issue.Comments = comments
	}
	return nil
}

// rowsNeedingComments is the page narrowed to the rows a comment read has to be
// issued for, and it is what bounds both that read and the plane lookup.
//
// A row the page can PROVE has no comments needs no query. Only a hydrated
// count proves it: under SkipCounts a zero means unknown, and treating it as
// none is how this body once dropped every comment a caller had asked for.
func rowsNeedingComments(items []*types.IssueWithCounts, countsKnown bool) []*types.IssueWithCounts {
	out := make([]*types.IssueWithCounts, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		if countsKnown && item.CommentCount == 0 {
			continue
		}
		out = append(out, item)
	}
	return out
}

func rowIDs(items []*types.IssueWithCounts) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
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
