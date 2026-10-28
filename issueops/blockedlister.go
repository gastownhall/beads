package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// BlockedIssue is one row of the blocked listing: the issue, plus the ids of
// the blockers bd names for it. It is the element type `bd blocked --json`
// emits.
type BlockedIssue = types.BlockedIssue

// BlockedRequest is the question `bd blocked` asks: which issues are blocked,
// optionally narrowed by parent and labels.
//
// Its fields are the command's flags, one for one. Labels are normalized the
// way every label filter is (trimmed, blanks and repeats dropped) on an
// attempt-local copy; the caller's slices are never written.
type BlockedRequest struct {
	// ParentID narrows to the parent's direct children (a parent-child edge)
	// and to ids under it in the dotted hierarchy. Empty means no narrowing.
	ParentID string
	// Labels must all be present (AND).
	Labels []string
	// LabelsAny needs at least one present (OR).
	LabelsAny []string
	// ExcludeLabels must all be absent.
	ExcludeLabels []string
}

// BlockedResult is the blocked listing.
type BlockedResult struct {
	// Items is ordered by priority ascending, then created_at descending,
	// then id. It is never nil: an empty listing is an empty slice, so a
	// front door that encodes it prints [] and never null.
	Items []*BlockedIssue
}

// BlockedLister describes listing the blocked issues: what `bd blocked`
// prints. Like Reader and ReadyCounter, it is a role with its own accessor. A
// new capability gets a new role interface and its own accessor; never append
// a method here.
//
// THE SET IS bd's BLOCKED DOOR, NOT THE RAW COLUMN. It is seeded from the
// stored, transitively propagated is_blocked flag on both planes (issues and
// wisps; closed and pinned-status rows excluded; no type exclusions), then
// narrowed to rows bd can NAME a blocker for: an active blocks, waits-for or
// conditional-blocks target resident in this workspace that is neither closed
// nor pinned, else the parent. A row whose flag is set but which has neither
// is not listed. Where the external-dependency policy applies, an unsatisfied
// external: blocker is added to the row's blockers, and a row blocked only by
// such refs is listed too, narrowed by the request's parent and label filters
// like every other row.
//
// IT IS NOT A PAGE. There is no limit and no cursor, like the local command,
// and an answer is never truncated.
//
// LISTING IS A READ. Nothing here records a history entry, fires a completion
// hook or changes a row. Result values are unspecified when error is non-nil.
type BlockedLister interface {
	// ListBlocked answers one blocked listing in one read.
	ListBlocked(ctx context.Context, req BlockedRequest) (BlockedResult, error)
}
