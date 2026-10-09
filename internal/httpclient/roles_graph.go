// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/roles_graph.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The four dependency-graph read roles, served from their v0 operations
// (design D8 rows 7-10).
//
// Each one is a thin projection, and that is a property of the wire rather than
// of the code: every response component on this surface is a Go TYPE ALIAS of
// the canonical struct the role returns (internal/httpapi/pinning.go asserts it
// by ASSIGNMENT, which a structurally-identical twin could not satisfy). So
// there is no field mapping to get wrong here, and deliberately none written —
// a hand-rolled converter would be a second copy of a contract the server
// already pins.
//
// What IS this layer's work is the contract each role's doc comment states and
// the wire does not restate: never-nil collections, the caller's request left
// alone, and the defensive caps the wire publishes no parameter for.
//
// They dispatch GENERICALLY, through the store's one door. A read role differs
// from its neighbors only in the parameters the encoder produced and the
// envelope it decodes, so an operation-shaped transport method per role would
// be a dozen spellings of one round trip — see the WireClient doc for why the
// two halves of that seam are shaped differently.

// httpCycleDetector serves DetectCycles from listDependencyCycles.
type httpCycleDetector struct{ store *Store }

// DetectCycles reports every cycle in the blocking graph.
//
// The request carries nothing (beyond IncludeTracks, refused below) and the
// operation publishes nothing, which is the same statement from both ends: a
// cycle is a property of the whole graph and there is no predicate that
// narrows it.
//
// req.IncludeTracks is refused rather than silently ignored: listDependencyCycles
// (wire.OpListDependencyCycles) has no include_tracks parameter at all, so
// honoring it would mean quietly answering the DEFAULT (narrower) walk to a
// caller who explicitly asked for the wider one that also follows `tracks`
// edges. This is the same typed, ledger-cited refusal CountEdges' own bound
// raises below (refuse/encode.RefusedError, not a bare error) — see
// "L-cycles-tracks" and engdocs/design/http-divergence-ledger.md.
func (d httpCycleDetector) DetectCycles(ctx context.Context, req issueops.DetectCyclesRequest) (result issueops.CycleReport, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role.
	defer func() { err = d.store.inexpressible("CycleDetector.DetectCycles", err) }()
	if req.IncludeTracks {
		return issueops.CycleReport{}, refuse(encode.OpListDependencyCycles, "L-cycles-tracks")
	}
	var body apigen.CyclesPage
	if err := d.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListDependencyCycles,
		Method: http.MethodGet,
		Path:   wire.PathDependenciesCycles,
	}, &body); err != nil {
		return issueops.CycleReport{}, err
	}
	// Empty rather than nil on an acyclic graph: the role promises a caller can
	// range over the answer, and the count of cycles is the length of this
	// slice with nothing omitted.
	cycles := body.Items
	if cycles == nil {
		cycles = []issueops.Cycle{}
	}
	return issueops.CycleReport{Cycles: cycles}, nil
}

// httpTreeWalker serves WalkTree from getDependencyTree.
type httpTreeWalker struct{ store *Store }

// WalkTree walks the graph from one root.
//
// TWO REQUEST MEMBERS ARE HONORED HERE RATHER THAN DROPPED. MaxDepth and
// Direction have parameters and travel; MaxRows does not, and it is the one
// member of this request whose contract says every implementation honors it —
// so the cap is enforced against the answer, where the role documents the check
// happening (after the walk and after the prune) and with the sentinel `bd`
// already classifies into exit 2.
//
// max_depth is sent ALWAYS, including the zero and the negative the role
// refuses. The operation defaults an ABSENT max_depth to fifty levels while the
// role makes the bound required, so omitting the caller's value would answer a
// fifty-level walk to a request that asked for none — a wider answer reached by
// dropping a bound, which is the one thing this client may never do.
//
// The validation refusals stay the SERVER's. An empty root, a direction outside
// the closed set and a depth below one are refusals the operation makes for
// itself and maps back to issueops.ErrValidation, so re-making them here would
// be a second copy of a rule that answers differently the day the vocabulary
// grows.
func (w httpTreeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	q := url.Values{}
	q.Set("root_id", req.RootID)
	q.Set("max_depth", strconv.Itoa(req.MaxDepth))
	if req.Direction != "" {
		q.Set("direction", string(req.Direction))
	}
	if req.Status != "" {
		q.Set("status", string(req.Status))
	}

	var body apigen.DependencyTreePage
	if err := w.store.dispatch(ctx, wire.Request{
		Op:      wire.OpGetDependencyTree,
		Method:  http.MethodGet,
		Path:    wire.PathDependenciesTree,
		Query:   q,
		IssueID: req.RootID,
	}, &body); err != nil {
		return issueops.TreeResult{}, err
	}

	nodes := make([]*issueops.TreeNode, 0, len(body.Items))
	for i := range body.Items {
		nodes = append(nodes, &body.Items[i])
	}
	if req.MaxRows > 0 && len(nodes) > req.MaxRows {
		// The count is EXACT rather than the storage layer's cap+1 probe: the
		// walk answers whole, so there is no cheaper bound to report and no
		// reason to report a vaguer one. The answer is withheld entirely, which
		// is what the role's own contract says a cap violation returns.
		return issueops.TreeResult{}, &storageops.ErrTooManyRows{
			Found:  len(nodes),
			Cap:    req.MaxRows,
			Source: req.MaxRowsSource,
		}
	}
	return issueops.TreeResult{Nodes: nodes}, nil
}

// httpEdgeReader serves ReadEdges from listDependencies.
type httpEdgeReader struct{ store *Store }

// ReadEdges reads the stored edges of one anchored set of ids.
//
// The wire answers with the edges FLATTENED across anchors plus the ids that
// named nothing; the role answers per anchor, in the order the request first
// named each id. Regrouping is this method's whole job, and it walks the
// CALLER's id list rather than the response — an anchor with no edges appears in
// neither `items` nor `missing`, so a grouping driven by the response alone
// would silently lose it.
//
// AN EMPTY REQUEST IS ANSWERED WITHOUT A DIAL. The operation requires at least
// one anchor and would refuse, but the role documents an empty request as a
// success with no anchors — so refusing would be this backend inventing an error
// the contract says does not exist.
func (r httpEdgeReader) ReadEdges(ctx context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	if err := validateAnchors(req.IDs); err != nil {
		return issueops.EdgeReadResult{}, err
	}
	ids := distinct(req.IDs)
	if len(ids) == 0 {
		return issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{}}, nil
	}
	// The ROLE has no anchor cap; the OPERATION refuses more than
	// maxEdgeReadAnchors per request. Splitting is therefore this transport's
	// job, never the caller's: each chunk is one round trip and the answers
	// concatenate in request order. A read loses nothing by being split — the
	// one property it gives up is the single snapshot across chunks, which
	// per-anchor answers do not rely on.
	if len(ids) > maxEdgeReadAnchors {
		anchors := make([]issueops.AnchorEdges, 0, len(ids))
		for start := 0; start < len(ids); start += maxEdgeReadAnchors {
			end := min(start+maxEdgeReadAnchors, len(ids))
			part, err := r.readEdgeChunk(ctx, ids[start:end], req.Types)
			if err != nil {
				return issueops.EdgeReadResult{}, err
			}
			anchors = append(anchors, part.Anchors...)
		}
		return issueops.EdgeReadResult{Anchors: anchors}, nil
	}
	return r.readEdgeChunk(ctx, ids, req.Types)
}

// maxEdgeReadAnchors is the listDependencies operation's cap on `issue_id`
// values (maxDependencyAnchors in internal/httpapi), redeclared because a
// client does not import the server.
const maxEdgeReadAnchors = 100

// readEdgeChunk answers at most maxEdgeReadAnchors distinct ids in one call.
func (r httpEdgeReader) readEdgeChunk(ctx context.Context, ids []string, edgeTypes []types.DependencyType) (issueops.EdgeReadResult, error) {
	req := issueops.EdgeReadRequest{IDs: ids, Types: edgeTypes}
	q := url.Values{}
	for _, id := range ids {
		q.Add("issue_id", id)
	}
	// Repeated rather than comma-joined: the operation reads `type` with the
	// repeatable list decoder and does no splitting, so a comma would travel
	// into a type name.
	for _, edgeType := range req.Types {
		q.Add("type", string(edgeType))
	}

	var body apigen.DependencyEdges
	if err := r.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListDependencies,
		Method: http.MethodGet,
		Path:   wire.PathDependencies,
		Query:  q,
	}, &body); err != nil {
		return issueops.EdgeReadResult{}, err
	}

	missing := make(map[string]bool, len(body.Missing))
	for _, id := range body.Missing {
		missing[id] = true
	}
	grouped := make(map[string][]*issueops.Dependency, len(ids))
	for i := range body.Items {
		edge := &body.Items[i]
		grouped[edge.IssueID] = append(grouped[edge.IssueID], edge)
	}

	anchors := make([]issueops.AnchorEdges, 0, len(ids))
	for _, id := range ids {
		edges := grouped[id]
		if edges == nil {
			// Never nil on a successful call, including for an anchor that has
			// no edges and for one that names nothing at all.
			edges = []*issueops.Dependency{}
		}
		anchors = append(anchors, issueops.AnchorEdges{ID: id, Edges: edges, Missing: missing[id]})
	}
	return issueops.EdgeReadResult{Anchors: anchors}, nil
}

// GetDependencyRecords answers one issue's stored outgoing edges as raw rows,
// off the same listDependencies operation httpEdgeReader rides. It is an off-role
// raw method rather than an accessor — `bd dep remove`, the molecule loaders and
// `bd delete`'s preview call it directly — so it sits here beside the surface it
// shares rather than on the role above.
//
// ONE ANCHOR, NEVER CHUNKED. The operation bounds a request at
// maxDependencyAnchors (100), and one id is nowhere near it, so there is no
// splitting to do. The wire flattens the answer across the anchors it was asked
// about; asked about one, every item belongs to that one, and the missing list
// names it iff the id resolved to nothing.
//
// A MISS IS (nil, nil), matching the local method: an id that names neither an
// issue nor a wisp comes back in `missing`, and the reference returns a nil slice
// for a nonexistent id (dolt/dependencies.go). The rows themselves are the wire's
// verbatim — apigen.Dependency is a type alias of types.Dependency, and the
// server resolves each row's target through the same issueops.DepTargetExpr the
// reference read does, so an unresolved or bare-slug depends_on_id (the value the
// GH#5005 `bd dep remove` guard exists to see) crosses whole rather than
// normalized away.
func (s *Store) GetDependencyRecords(ctx context.Context, id string) ([]*types.Dependency, error) {
	if id == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("issue_id", id)

	var body apigen.DependencyEdges
	if err := s.dispatch(ctx, wire.Request{
		Op:     wire.OpListDependencies,
		Method: http.MethodGet,
		Path:   wire.PathDependencies,
		Query:  q,
	}, &body); err != nil {
		return nil, err
	}

	for _, missing := range body.Missing {
		if missing == id {
			return nil, nil
		}
	}
	if len(body.Items) == 0 {
		return nil, nil
	}
	records := make([]*types.Dependency, 0, len(body.Items))
	for i := range body.Items {
		records = append(records, &body.Items[i])
	}
	return records, nil
}

// maxEdgeCountAnchors is the anchor bound GET /v0/beads/dependencies:count
// publishes: `issue_id` is minItems 1, maxItems 100.
//
// It is the SAME number as the stored-edge read's, from the server's one
// maxDependencyAnchors constant, because the two operations bound the same
// thing on the same collection — which is exactly why the client refuses at it
// rather than chunking past it. See httpGraphCounter.CountEdges.
const maxEdgeCountAnchors = 100

// GetAllDependencyRecords answers storage.DoltStorage's broad, unfiltered
// dependency dump: every stored edge, grouped by its owning issue's id — the
// same shape issueops.GetAllDependencyRecordsInTx returns for a local
// backend.
//
// It overrides the unsupported_gen.go stub (design 3.6, HIGH-1): the
// external-deps decorator's loadBlockingState falls back to this method
// whenever the inner store implements neither
// storage.ExternalDependencyPolicyProber-reported server enforcement nor the
// narrower storage.ExternalDependencyQueryStore, which is exactly this
// backend's shape against a server that does NOT advertise
// wire.CapExternalDependencies. Refusing here (the old unsupported behavior)
// would make `bd ready`/`bd close`/claim hard-fail against such a server
// instead of correctly enforcing the policy client-side — the regression
// HIGH-1 reports.
//
// There is no single wire operation for "every dependency in the workspace":
// listDependencies is anchored at up to maxEdgeCountAnchors ids per call, so
// this walks every issue id over the full-exhaustion list (allIssueIDs) and
// reads its edges back in chunks of that size. It is O(issues/100) requests,
// which only this fallback path ever pays — a server that advertises the
// capability, or that implements the narrower query, never reaches this
// method.
func (s *Store) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	ids, err := s.allIssueIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all issue ids: %w", err)
	}
	result := make(map[string][]*types.Dependency)
	for _, chunk := range chunkStrings(ids, maxEdgeCountAnchors) {
		q := url.Values{}
		for _, id := range chunk {
			q.Add("issue_id", id)
		}
		var body apigen.DependencyEdges
		if err := s.dispatch(ctx, wire.Request{
			Op:     wire.OpListDependencies,
			Method: http.MethodGet,
			Path:   wire.PathDependencies,
			Query:  q,
		}, &body); err != nil {
			return nil, err
		}
		for i := range body.Items {
			edge := &body.Items[i]
			result[edge.IssueID] = append(result[edge.IssueID], edge)
		}
	}
	return result, nil
}

// allIssueIDs enumerates every issue id in the workspace, regardless of
// status or kind: closed, templates, gates, infra and ephemeral/wisp issues
// are all included (the list operation's narrower defaults otherwise exclude
// several of those), because a dependency row can be owned by any of them and
// GetAllDependencyRecords promises the whole table, not the default `bd
// list` view of it.
//
// want=0, maxRows=0 walks fetchIssuePages to full exhaustion rather than one
// page.
func (s *Store) allIssueIDs(ctx context.Context) ([]string, error) {
	q := url.Values{
		"all":               {"true"},
		"include_templates": {"true"},
		"include_gates":     {"true"},
		"include_infra":     {"true"},
		"include_ephemeral": {"true"},
	}
	rows, err := s.fetchIssuePages(ctx, q, true, 0, 0, "", nil)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Issue == nil || row.ID == "" {
			continue
		}
		ids = append(ids, row.ID)
	}
	return ids, nil
}

// chunkStrings splits ids into slices of at most size, preserving order. size
// must be positive; the last chunk may be shorter.
func chunkStrings(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(ids)+size-1)/size)
	for len(ids) > 0 {
		n := size
		if n > len(ids) {
			n = len(ids)
		}
		chunks = append(chunks, ids[:n])
		ids = ids[n:]
	}
	return chunks
}

// httpGraphCounter serves CountEdges from countDependencyEdges — the
// TWENTY-SECOND wire-backed accessor, and the only role on this surface whose
// answer is a number PER ANCHOR.
//
// It builds its query inline rather than through the encoder table, beside the
// three graph reads above and for their reason: the encode package classifies
// PREDICATES, where a forgotten member widens a result set invisibly, and this
// request is an anchored one — four members, every one of them published. What
// takes the place of the table here is TestEdgeCountRequestSendsEveryMember,
// which reflects over the request so a member added upstream fails closed
// instead of being dropped into a count that still looks like a count.
type httpGraphCounter struct{ store *Store }

// CountEdges returns each anchor's edge cardinality in the requested direction.
//
// THE VALIDATION IS THE ROLE'S OWN BODY, not a second copy of it: this calls
// storageops.ValidateEdgeCountRequest, the same function the two Dolt legs and
// the unit-of-work leg run, so the four refusals — an absent or unrecognized
// direction, a status beside direction=out, an empty anchor id, an unusable
// dependency type — are one definition with one ORDER. The order is part of the
// contract (the direction is checked FIRST, so an empty request refuses about
// the direction rather than answering emptily), and a client that re-asked the
// questions in some order of its own would name a different offender than every
// other backend for a request carrying two mistakes.
//
// AN EMPTY REQUEST IS ANSWERED WITHOUT A DIAL, exactly as the stored-edge read's
// is: the operation requires at least one anchor and would refuse, while the
// role documents an empty slice as a success with no anchors. The direction is
// still validated first, so EdgeCountRequest{} refuses rather than answering.
func (c httpGraphCounter) CountEdges(ctx context.Context, req issueops.EdgeCountRequest) (issueops.EdgeCountResult, error) {
	if err := storageops.ValidateEdgeCountRequest(req); err != nil {
		return issueops.EdgeCountResult{}, err
	}
	// The collapse is the CLIENT's, not the wire's, and it has to be: the answer
	// carries one entry per distinct anchor in first-mention order, and matching
	// the response back onto the caller's list means holding the same collapse
	// the server performed. Sending the repeats and trusting the server to
	// collapse them would work until an id appeared twice near the bound, where
	// the request would refuse for a size the answer never had.
	ids := distinct(req.IDs)
	if len(ids) == 0 {
		return issueops.EdgeCountResult{Anchors: []issueops.AnchorEdgeCount{}}, nil
	}
	if len(ids) > maxEdgeCountAnchors {
		return issueops.EdgeCountResult{}, &encode.RefusedError{
			Op:  encode.OpCountDependencyEdges,
			Row: encode.RowByID("L-edgecount-bound"),
		}
	}

	q := url.Values{}
	for _, id := range ids {
		q.Add("issue_id", id)
	}
	// Required and always sent, including a value the role has already accepted:
	// the operation has no default direction, and an omitted one is a 400 rather
	// than a count of the other end.
	q.Set("direction", string(req.Direction))
	// Repeated rather than comma-joined, listDependencies' rule: the operation
	// reads `type` with the repeatable list decoder and does no splitting.
	for _, edgeType := range req.Types {
		q.Add("type", string(edgeType))
	}
	if req.Status != "" {
		q.Set("status", req.Status)
	}

	var body apigen.EdgeCounts
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpCountDependencyEdges,
		Method: http.MethodGet,
		Path:   wire.PathDependenciesCount,
		Query:  q,
	}, &body); err != nil {
		return issueops.EdgeCountResult{}, err
	}
	return edgeCountAnchors(ids, body)
}

// edgeCountAnchors reads the per-anchor answer back onto the caller's own list.
//
// IT WALKS THE REQUEST, NOT THE RESPONSE, for the reason httpEdgeReader's
// regrouping does — the order and the membership of the answer are the
// REQUEST's, and a projection driven by the body would inherit whatever the
// server happened to send. Here that is sharper than usual, because the two
// facts this role publishes are a COUNT and a MISSING flag, and both have a
// plausible-looking default: a dropped entry reads as 0, and a lost flag reads
// as an anchor that exists.
//
// So an anchor the answer does not carry is an ERROR rather than a zero. The
// operation promises one entry per distinct requested id; a body missing one is
// a server that did not answer the question, and the two readings a client could
// substitute — count 0, or missing — are the two answers this role exists to
// tell apart.
func edgeCountAnchors(ids []string, body apigen.EdgeCounts) (issueops.EdgeCountResult, error) {
	answered := make(map[string]apigen.AnchorEdgeCount, len(body.Anchors))
	for _, anchor := range body.Anchors {
		answered[anchor.Id] = anchor
	}
	anchors := make([]issueops.AnchorEdgeCount, 0, len(ids))
	for _, id := range ids {
		entry, ok := answered[id]
		if !ok {
			return issueops.EdgeCountResult{}, fmt.Errorf(
				"bd serve answered an edge count with no entry for anchor %q: the operation carries one per distinct requested id, "+
					"and an absent one cannot be told from a count of 0 or from an anchor that is not there", id)
		}
		// Count is int64 on the wire, in apigen and on the role: a workspace's
		// graph is not bounded by 2^53, and a narrowed read would answer a
		// number NEAR the cardinality.
		anchors = append(anchors, issueops.AnchorEdgeCount{ID: id, Count: entry.Count, Missing: entry.Missing})
	}
	return issueops.EdgeCountResult{Anchors: anchors}, nil
}

// httpBlockingAnnotator serves AnnotateBlocking from listBlockingAnnotations.
type httpBlockingAnnotator struct{ store *Store }

// AnnotateBlocking decorates one anchored set of ids.
//
// The wire already answers one entry per distinct id in first-mention order, so
// the projection is the identity — but the never-nil promise on the two lists is
// this layer's, because a JSON `[]` decodes to a non-nil empty slice while an
// absent member decodes to nil, and the role promises a caller may range over
// both without checking.
func (a httpBlockingAnnotator) AnnotateBlocking(ctx context.Context, req issueops.BlockingRequest) (issueops.BlockingResult, error) {
	if err := validateAnchors(req.IDs); err != nil {
		return issueops.BlockingResult{}, err
	}
	ids := distinct(req.IDs)
	if len(ids) == 0 {
		return issueops.BlockingResult{Items: []issueops.IssueBlocking{}}, nil
	}

	q := url.Values{}
	for _, id := range ids {
		q.Add("issue_id", id)
	}

	var body apigen.BlockingAnnotations
	if err := a.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListBlockingAnnotations,
		Method: http.MethodGet,
		Path:   wire.PathDependenciesBlocking,
		Query:  q,
	}, &body); err != nil {
		return issueops.BlockingResult{}, err
	}

	items := make([]issueops.IssueBlocking, 0, len(body.Items))
	for _, item := range body.Items {
		if item.BlockedBy == nil {
			item.BlockedBy = []string{}
		}
		if item.Blocks == nil {
			item.Blocks = []string{}
		}
		items = append(items, item)
	}
	return issueops.BlockingResult{Items: items}, nil
}

// distinct collapses repeated anchors, keeping each at the position of its first
// mention — the order both roles answer in.
//
// It COPIES rather than sorting or compacting in place: the caller's slice is
// theirs, and both roles promise not to touch it.
func distinct(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// validateAnchors refuses an empty anchor id, and does so BEFORE the collapse.
//
// It is the one validation these two roles make for themselves, because the
// collapse would otherwise hide it in both directions: an empty entry alongside
// a real one would simply not be sent, and a request of nothing but empty
// entries would reach the empty-request arm looking like a request that named
// nothing. Those are different questions — an empty entry is ErrValidation, an
// empty slice is a success — and the server can only refuse what it receives.
func validateAnchors(ids []string) error {
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: an anchor id must not be empty", issueops.ErrValidation)
		}
	}
	return nil
}
