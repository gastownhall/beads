package issueops

// SearchListRequest is `bd search`'s request: the listing that answers the
// text query, with search's own scope. Every route of `bd search` — the direct
// store, the proxied server and a remote backend — asks issueops.Reader.List
// with this request (plus the caller's narrowing flags), so the three cannot
// answer the same search differently, and a remote backend serves it through
// the same role the server's listIssues handler calls.
//
// The scope is search's, not a listing's:
//   - EVERY STATUS. Closed rows match unless the caller narrows with Status
//     (bd-t5yex: "was this already filed or fixed?" must not answer no just
//     because the answer is closed). AllFlag lifts the status exclusions and
//     the pinned-flag predicate together.
//   - EVERY KIND OF ROW. Templates, gates, the workspace's infrastructure types
//     and the ephemeral plane all match; the four Include* members lift those
//     exclusions.
//   - THE STORE'S DEFAULT ORDER, priority then newest then id, so a Limit
//     keeps the same rows a raw SearchIssues read kept. A display sort is the
//     caller's to apply to the page afterwards.
//
// Limit is left nil (the listing default); a caller sets it.
func SearchListRequest(query string) ListRequest {
	return ListRequest{
		Query:            query,
		AllFlag:          true,
		IncludeTemplates: true,
		IncludeGates:     true,
		IncludeInfra:     true,
		IncludeEphemeral: true,
		SortBy:           "priority",
	}
}
