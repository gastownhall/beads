package httpapi

import (
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time proof that the generated wire types ARE the canonical structs,
// not mirrors of them. There is no Go-to-Go mapping layer on this surface: the
// CLI's --json output, the JSONL interchange and the HTTP bodies all marshal
// one struct, so a new types.Issue field appears on the wire automatically
// instead of silently vanishing.
//
// These assignments (not conversions — a conversion between two structurally
// identical structs compiles) fail the BUILD if oapi-codegen ever emits a
// mirror struct or aliases a schema to the wrong type. That is not
// hypothetical: v2.6.0 drops a component's x-go-type when the component also
// carries allOf, and an allOf-composed IssueWithCounts generated
// `type IssueWithCounts = types.Issue` — the ready endpoint would have shipped
// bare issues with the count fields silently gone. The spec repeats property
// lists instead of composing for exactly this reason (see the note at the top
// of internal/httpapi/spec/openapi.v0.yaml); this file is what makes a
// regression loud.
var (
	_ apigen.Issue                       = types.Issue{}
	_ apigen.IssueWithCounts             = types.IssueWithCounts{}
	_ apigen.IssueDetails                = types.IssueDetails{}
	_ apigen.IssueWithDependencyMetadata = types.IssueWithDependencyMetadata{}
	_ apigen.Dependency                  = types.Dependency{}
	_ apigen.Comment                     = types.Comment{}
	_ apigen.BondRef                     = types.BondRef{}

	// The envelopes carry the canonical types too — pinning the schema is not
	// enough if a page's items resolve to something else.
	_ []types.IssueWithCounts = apigen.ReadyPage{}.Items
	_ []types.IssueWithCounts = apigen.IssuesPage{}.Items
	_ types.Issue             = apigen.ClaimResponse{}.Issue
)
