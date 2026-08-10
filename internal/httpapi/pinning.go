package httpapi

import (
	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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
	_ apigen.TreeNode                    = types.TreeNode{}
	_ apigen.Comment                     = types.Comment{}
	_ apigen.BondRef                     = types.BondRef{}
	_ apigen.Statistics                  = types.Statistics{}

	// The cycle pair is pinned to the ROLE package rather than to
	// internal/types, because that is where its canonical structs live: the
	// role's result IS what `bd dep cycles --json` marshals.
	_ apigen.Cycle       = issueops.Cycle{}
	_ apigen.CycleMember = issueops.CycleMember{}

	// Same reason, one role over: BlockingAnnotator's result element IS what
	// `bd list` renders its decoration from.
	_ apigen.IssueBlocking = issueops.IssueBlocking{}

	// The journal record is pinned to neither internal/types nor a role package
	// but to the journal's own leaf: eventsjournal.Record is what
	// `bd events tail` prints one of per line, and the committed golden fixture
	// is a byte-level pin on that encoding. This assignment is what stops a
	// regenerated mirror from quietly giving GET /v0/beads/events a record shape
	// the golden never sees.
	_ apigen.EventRecord = eventsjournal.Record{}

	// The envelopes carry the canonical types too — pinning the schema is not
	// enough if a page's items resolve to something else.
	_ []types.IssueWithCounts = apigen.ReadyPage{}.Items
	_ []types.IssueWithCounts = apigen.IssuesPage{}.Items
	_ []types.IssueWithCounts = apigen.QueryPage{}.Items
	_ types.Issue             = apigen.ClaimResponse{}.Issue
	_ types.Statistics        = apigen.StatsResponse{}.Summary
	_ []issueops.Cycle        = apigen.CyclesPage{}.Items
	_ []types.TreeNode        = apigen.DependencyTreePage{}.Items

	// The envelopes that are NOT pages resolve their items the same way and are
	// pinned for the same reason: one whose items generated as a mirror struct
	// would put a second wire shape for one fact on this surface, silently.
	// DependencyEdges is here because wave 2 shipped without it (bd-5o3gt);
	// every envelope on this surface whose items are a canonical struct is now
	// pinned.
	_ []types.Dependency       = apigen.DependencyEdges{}.Items
	_ []issueops.IssueBlocking = apigen.BlockingAnnotations{}.Items
	_ []types.Issue            = apigen.BatchCreateResponse{}.Items

	_ []eventsjournal.Record = apigen.EventsPage{}.Records
)

// SettingsPage.Items and BatchCreateRequest.Items are deliberately absent:
// their element schemas (Setting, BatchCreateItem) carry no x-go-type, because
// neither has a canonical Go struct whose JSON encoding is that contract. There
// is nothing to weld them to, so a pin here would assert a compatibility domain
// that does not exist.
