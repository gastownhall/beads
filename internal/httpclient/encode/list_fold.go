package encode

import (
	"strings"

	"github.com/steveyegge/beads/issueops"
)

// FoldTitleContains rewrites ListRequest.TitleContains onto TitleSearch when
// the pair compiles to one clause, so `bd list --title-contains X` rides the
// listing's existing `title` parameter (behind issues.list.title) instead of
// refusing.
//
// The fold is exact, not an approximation: the store compiles both members to
// the identical predicate, `LOWER(title) LIKE '%' || lower(v) || '%'`
// (internal/storage/sqlbuild, which lowers v with strings.ToLower), and
// workapi.BuildListFilter copies each straight through. So when TitleSearch is
// empty, or equal to TitleContains once both are lowered the same way, the
// request without TitleContains selects exactly the same rows. The comparison
// is strings.ToLower equality rather than strings.EqualFold because only the
// former is the store's own lowering: EqualFold also matches pairs such as
// "ſ" and "s", which lower to different patterns.
//
// When both are set and differ the request is returned unchanged: the leftover
// TitleContains rides `title_contains` beside `title`, behind
// issues.list.filters (ListFiltersMembers), so neither half is dropped.
//
// The request is taken and returned by value; the caller's copy is never
// mutated.
func FoldTitleContains(req issueops.ListRequest) issueops.ListRequest {
	if req.TitleContains == "" {
		return req
	}
	switch {
	case req.TitleSearch == "":
		req.TitleSearch, req.TitleContains = req.TitleContains, ""
	case strings.ToLower(req.TitleSearch) == strings.ToLower(req.TitleContains):
		req.TitleContains = ""
	}
	return req
}
