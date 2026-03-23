package issueops

import "github.com/steveyegge/beads/internal/types"

// WispFilterToIssueFilter converts a types.WispFilter into an IssueFilter
// suitable for use with SearchIssuesInTx or searchTableInTx.
// The returned filter always has Ephemeral=true so queries are routed to the
// wisps table; callers do not need to set that flag.
func WispFilterToIssueFilter(f types.WispFilter) types.IssueFilter {
	ephemeral := true
	filter := types.IssueFilter{
		Ephemeral:     &ephemeral,
		IssueType:     f.Type,
		Status:        f.Status,
		UpdatedAfter:  f.UpdatedAfter,
		UpdatedBefore: f.UpdatedBefore,
		Limit:         f.Limit,
	}
	// When no explicit status filter is set and IncludeClosed is false,
	// exclude closed wisps. This matches the default behavior of
	// "bd mol wisp list" (which hides closed wisps unless --all is passed).
	if !f.IncludeClosed && f.Status == nil {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
	}
	return filter
}
