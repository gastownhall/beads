package graphops

import "context"

// The Reader role: one record by path, keyset pages under an opaque cursor,
// incident Links. PROTECTED — every call reloads this workspace's witness and
// asserts it inside the read transaction (Scope row identity, ledger head, an
// unexpired lease it never writes, the graph-state version); a workspace that
// is not the authority answers ErrNotAuthority, one with no Scope answers
// ErrNoScope, and a rewound or unverified witness refuses with its own
// sentinel. Reads never write, never set the role variable, and never wake
// anything.
//
// Every request is a high-level question: a path, a Type, a cursor, a limit.
// No request carries authority. Limits are bounded by the store's page bound
// and defaulted when zero; a cursor the store did not mint is refused.

// BeadRequest names one Bead by canonical path.
type BeadRequest struct {
	// Path is the canonical Scope-relative Bead path ("beads/…"). A
	// non-canonical spelling is ErrValidation, never resolved.
	Path string
}

// LinkRequest names one Link by canonical path.
type LinkRequest struct {
	// Path is the canonical Scope-relative Link path ("links/…").
	Path string
}

// BeadSelectRequest describes one keyset page of the Bead collection.
type BeadSelectRequest struct {
	// TypeURL restricts to Beads of exactly this declared Type; "" selects
	// every Bead. Conformance-closure selection is not a P0/P1 surface.
	TypeURL string
	// After continues a page from the cursor the previous page returned; ""
	// starts at the beginning.
	After Cursor
	// Limit bounds the page; 0 asks for the store's default.
	Limit int
}

// LinkSelectRequest describes one keyset page of the Link collection.
type LinkSelectRequest struct {
	// TypeURL restricts to Links of exactly this declared Type; "" selects
	// every Link.
	TypeURL string
	// Source restricts to Links leaving this reference — an in-Scope Bead or
	// an external URI — compared by URI alone (a pin adds no identity); nil
	// does not restrict. The endpoints are symmetric (B2, P0 council
	// 2026-09-07): a Link's source may be external exactly as its target may.
	Source *Ref
	// Target restricts to Links pointing at this reference, compared by URI
	// alone; nil does not restrict.
	Target *Ref
	// After continues a page; "" starts at the beginning.
	After Cursor
	// Limit bounds the page; 0 asks for the store's default.
	Limit int
}

// IncidentRequest describes one page of the Links incident to a Bead.
type IncidentRequest struct {
	// Path is the canonical path of the Bead.
	Path string
	// Direction selects inbound, outbound or both; the zero value is both,
	// the protocol's default.
	Direction Direction
	// After continues a page; "" starts at the beginning.
	After Cursor
	// Limit bounds the page; 0 asks for the store's default.
	Limit int
}

// Reader is the read question over one Scope, answered in one transaction
// per call from this workspace's authoritative state.
type Reader interface {
	// Bead returns one Bead with its complete ownedLinks expansion, assembled
	// in the same snapshot — the groups BeadRecord documents: one per Link
	// Type the Bead's Type declares explicitly, empty groups included, plus
	// one per wildcard-owned Type actually present, in code-unit order of
	// Type URL, each group's Links in code-unit order of path (the law is
	// CheckBeadRecord). A path that never existed or
	// is not visible is ErrNotFound; a path in a gone state is a *GoneError,
	// which also matches ErrNotFound.
	Bead(ctx context.Context, req BeadRequest) (BeadRecord, error)
	// Link returns one Link. Not-found and gone are as for Bead.
	Link(ctx context.Context, req LinkRequest) (Link, error)
	// Beads returns one page of BeadRecords in ascending code-unit order of
	// path — the canonical-uri baseline order — continuing after the cursor.
	// The page's Next is "" after the last page. Cross-request snapshot
	// continuation is P2's cursor ADR; until it, a cursor continues the
	// selection, not a snapshot.
	Beads(ctx context.Context, req BeadSelectRequest) (BeadPage, error)
	// Links returns one page of Links in ascending code-unit order of path,
	// under the request's structural predicates combined with AND.
	Links(ctx context.Context, req LinkSelectRequest) (LinkPage, error)
	// IncidentLinks returns one page of the Links whose source (out), target
	// (in) or either (both) is the Bead at req.Path, in ascending code-unit
	// order of path: one ordered, limited union over the two endpoint
	// indexes. Only an in-Scope Bead has this view. An unknown Bead is
	// ErrNotFound.
	IncidentLinks(ctx context.Context, req IncidentRequest) (LinkPage, error)
}
