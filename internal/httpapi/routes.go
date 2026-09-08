package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

const (
	// customMethodPathValue names the ServeMux wildcard the single-resource
	// custom methods share. The route rows build their pattern from it and
	// customMethodTarget splits the custom method back off the segment it
	// matched, so the two halves of the one path exception cannot drift apart.
	customMethodPathValue = "idop"
	// customMethodPattern is that one registration. Every row carrying a
	// customMethod declares it, and s.handler collapses them into a single
	// ServeMux entry.
	customMethodPattern = "/v0/beads/issues/{" + customMethodPathValue + "}"
	// customMethodIDValue is where the dispatcher leaves the id it split out,
	// so a handler reads its target exactly as one on a literal `{id}` pattern
	// does rather than re-deriving the split.
	customMethodIDValue = "id"
)

// ProjectIDHeader names the optional per-request workspace-identity stamp. A
// client that knows which workspace it means to address puts that workspace's
// project id here; checkProjectStamp (server.go) refuses the request when the id
// it names is not the one this server serves, so a misdirected read or write is
// turned away before it can touch the wrong workspace. The header is optional and
// absent by default: an older client that never sends it is served exactly as
// before, which is what keeps enforcement additive rather than a new precondition.
const ProjectIDHeader = "Bd-Project-Id"

// CapProjectEnforce is the behavior capability that advertises this enforcement.
// Unlike the per-operation tokens it names no route: it tells a client that a
// stamped request WILL be checked here, so the client can rely on the refusal
// instead of discovering an older server silently ignored its stamp.
const CapProjectEnforce = "project.enforce"

// customMethodTarget splits the custom method off the segment the router
// matched, and reports the row that claims it.
//
// ServeMux wildcards match a whole path segment, so `{id}:claim` is not
// expressible as a pattern: the rows register one shared wildcard and the parse
// lands here. A segment that ends in no REGISTERED suffix is not an addressable
// resource on this surface — POST on the issue-detail path is documented
// nowhere — so it gets the same 404 the catch-all gives any other unrouted
// path. That is what keeps the wide pattern a routing detail rather than
// undocumented surface.
//
// The id itself is bounded HERE, once for every operation on the pattern, for
// the same reason the actor is bounded at the edge: this is the last point
// before a request buys a concurrency slot and two database round trips.
// `issues.id` is VARCHAR(255) and the document calls the parameter an exact
// canonical id, so a longer one — or one carrying a control character, which a
// percent-escape in the path decodes to — names no row that can exist.
func customMethodTarget(rows []route, segment string) (route, string, *Result) {
	unrouted := func() *Result {
		res := newResult(CodeNotFound, "no such route on this server")
		return &res
	}
	for _, rt := range rows {
		id, ok := strings.CutSuffix(segment, rt.customMethod)
		if !ok || id == "" {
			continue
		}
		if types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
			// The SAME 404 a real miss gets. A distinct refusal here would let
			// a caller map the server's notion of a well-formed id, and there
			// is nothing to learn from it: no such row exists either way.
			res := NotFound()
			return route{}, "", &res
		}
		return rt, id, nil
	}
	return route{}, "", unrouted()
}

// route is one row of the surface: an operation, where it lives in the
// document, where it lives in the router, and what the request lifecycle owes
// it. TestSpecRouteParity compares this table against the spec's paths, and
// s.handler builds the router from it, so "the routes" and "the document" are
// the same statement made twice.
type route struct {
	// op is the spec's operationId. It also names the operation in the
	// request log line.
	op string
	// method and pattern are what the router registers.
	method string
	// pattern is the ServeMux pattern. It usually equals specPath, and must
	// not be assumed to: see the claim row.
	pattern string
	// specPath is the path as the DOCUMENT spells it. Declared, never derived
	// from pattern, because the two genuinely differ for the custom-method
	// rows below and a derivation would have to encode that exception.
	specPath string
	// customMethod is the `:verb` this row answers on a SHARED wildcard
	// pattern, or "" for a row the router can spell literally.
	//
	// ServeMux wildcards match a whole segment, so `{id}:close` is not
	// expressible as a pattern — and the single-resource custom methods
	// COLLIDE: three rows cannot each register POST /v0/beads/issues/{idop}.
	// Rows carrying this field share one registration; the dispatcher splits
	// the trailing suffix off the matched segment and hands the request to the
	// row that claims it (see customMethodTarget).
	//
	// A row with a customMethod must declare its specPath, because the whole
	// point is that the router cannot spell the documented path.
	customMethod string
	// capability is the token this operation contributes to
	// ContextResponse.capabilities, or "" for operations outside that
	// vocabulary. A stub contributes nothing whatever this says.
	capability string
	// bypassSemaphore exempts an operation from the request-wide database slot.
	// Legitimate for handlers that touch no database — liveness and identity
	// must stay answerable while every slot is held by a long scan — and for a
	// streaming row, which takes a slot around each of its reads instead. It is
	// never a way to skip the limit while touching the database.
	bypassSemaphore bool
	// streaming marks an operation whose response is held open indefinitely
	// rather than written and finished. Such a row is exempt from
	// requestDeadline, which for every other operation is the backstop that
	// stops a request from holding resources forever and here would simply cut
	// the stream off mid-flight; the handler bounds its own reads and exits on
	// client disconnect or shutdown (see streamEvents).
	//
	// A streaming row must also set bypassSemaphore: holding one of the sixteen
	// database slots for the life of a connection is the starvation the deadline
	// used to prevent, so the slot moves to the individual reads.
	// TestStreamingRowsAreTheDocumentsStreamingOps pins the pair.
	streaming bool
	// authExempt serves an operation with no bearer credential on a server
	// that was configured with one. Only legitimate for liveness: the probe
	// must answer with no credential, and it discloses nothing but that the
	// process is up. Identity is NOT exempt — GET /v0/beads/context reveals the
	// repo root, the beads directory and the database name.
	//
	// It is a column rather than a middleware rule for the same reason
	// bypassSemaphore is: the exemption is a property of the operation, so it
	// belongs where TestSpecSecurityMatchesRouteTable can compare it against
	// the document's per-operation `security` declarations.
	authExempt bool
	// projectExempt exempts an operation from the Bd-Project-Id stamp check
	// (checkProjectStamp). Legitimate only for liveness and the identity
	// handshake: liveness must answer whatever workspace the caller thinks it
	// reached, and the handshake is where a client LEARNS the project id to
	// stamp with — gating it on a matching stamp would make the project id
	// undiscoverable to a client that does not already have it. It is NOT
	// coupled to bypassSemaphore: a bypassSemaphore row such as events:watch
	// carries journal data and stays project-stamp-ENFORCED, so exactly the two
	// reads that touch no workspace data are exempt and every other route —
	// streaming or not — is enforced.
	projectExempt bool
	// implemented gates the capability list, so a release between slices never
	// advertises an operation that does not work. Every v0 operation is
	// implemented as of the read-endpoints slice; the flag stays because the
	// next operation to be added will arrive stubbed, and because
	// TestSpecStatusCodesMatchHandlerTable fails if a stub ever reappears
	// without the 501 exemption that documents it.
	implemented bool
	handler     func(*Server, http.ResponseWriter, *http.Request)
}

// routeTable is the whole surface. Every operation in the spec appears here,
// so route/spec parity is a single all-at-once check rather than a list that
// grows a per-slice exemption each time. As of the read-endpoints slice every
// row is implemented and none is a 501 stub.
var routeTable = []route{
	{
		op:      OpHealth,
		method:  http.MethodGet,
		pattern: "/healthz",
		// Liveness answers from the process and touches nothing that can
		// fail, so it must not queue behind the database. That is exactly
		// what makes it liveness-only: it stays green while Dolt is wedged.
		bypassSemaphore: true,
		// The one auth-exempt row. A kubelet probe presents no credential, and
		// a liveness endpoint that 401s is a pod that restarts forever.
		authExempt: true,
		// And it answers whatever workspace the caller believed it reached: a
		// liveness probe gated on a matching project stamp would go dark on a
		// misconfigured client exactly when an operator needs it most.
		projectExempt: true,
		implemented:   true,
		handler:       (*Server).handleHealth,
	},
	{
		op:      OpGetContext,
		method:  http.MethodGet,
		pattern: "/v0/beads/context",
		// A startup snapshot, so it touches no database either — and identity
		// staying observable under saturation is half of how an operator
		// tells one wedged server from another.
		bypassSemaphore: true,
		// It is also where a client LEARNS this server's project id, so it
		// cannot itself require a matching stamp: the handshake that hands out
		// the id must answer before the client has one to send.
		projectExempt: true,
		implemented:   true,
		handler:       (*Server).handleContext,
	},
	{
		op:          OpListReadyWork,
		method:      http.MethodGet,
		pattern:     "/v0/beads/ready",
		capability:  "ready.list",
		implemented: true,
		handler:     (*Server).handleReady,
	},
	{
		op:     OpGetStats,
		method: http.MethodGet,
		// `stats`, not `status`: on an HTTP surface `status` reads as the
		// server's own condition, which is /healthz. This one answers about the
		// workspace and takes a database slot to do it.
		pattern:     "/v0/beads/stats",
		capability:  "stats.get",
		implemented: true,
		handler:     (*Server).handleStats,
	},
	{
		op:          OpListDependencyCycles,
		method:      http.MethodGet,
		pattern:     "/v0/beads/dependencies/cycles",
		capability:  "dependencies.cycles",
		implemented: true,
		handler:     (*Server).handleDependencyCycles,
	},
	{
		op:     OpCountReadyWork,
		method: http.MethodGet,
		// A collection-level custom method, spelled the way the claim route's
		// is. Unlike that one it needs no specPath declaration: the segment is
		// a LITERAL, and only a wildcard segment is inexpressible as a ServeMux
		// pattern, so the router registers the documented path itself.
		pattern:     "/v0/beads/ready:count",
		capability:  "ready.count",
		implemented: true,
		handler:     (*Server).handleCountReady,
	},
	{
		op:          OpListIssues,
		method:      http.MethodGet,
		pattern:     "/v0/beads/issues",
		capability:  "issues.list",
		implemented: true,
		handler:     (*Server).handleListIssues,
	},
	{
		op:     OpQueryIssues,
		method: http.MethodGet,
		// A collection-level custom method, spelled as ready:count is.
		pattern:     "/v0/beads/issues:query",
		capability:  "issues.query",
		implemented: true,
		handler:     (*Server).handleQueryIssues,
	},
	{
		op:     OpCountIssues,
		method: http.MethodGet,
		// A collection-level custom method on the issue collection, spelled the
		// way ready:count's is: both segments are LITERAL, so pattern and
		// specPath agree and the router registers the documented path itself.
		//
		// It cannot collide with the claim's wide POST wildcard — that one is
		// registered under POST and requires the separating slash this path has
		// none of — nor with the plain collection GET, which ServeMux matches
		// whole.
		pattern:     "/v0/beads/issues:count",
		capability:  "issues.count",
		implemented: true,
		handler:     (*Server).handleCountIssues,
	},
	{
		op:          OpGetIssue,
		method:      http.MethodGet,
		pattern:     "/v0/beads/issues/{id}",
		capability:  "issues.get",
		implemented: true,
		handler:     (*Server).handleGetIssue,
	},
	{
		op:     OpUpdateIssue,
		method: http.MethodPatch,
		// A PLAIN METHOD on the issue-detail path, not a custom method: partial
		// update of one named resource is what PATCH already means, exactly as
		// one named resource with no body is what DELETE means for forgetMemory.
		//
		// It therefore registers directly and keeps pattern == specPath. It
		// cannot collide with the POST dispatcher above — ServeMux registers
		// method and pattern together — and it leaves the custom-method
		// namespace for the operations that are not CRUD.
		pattern:     "/v0/beads/issues/{id}",
		capability:  "issues.update",
		implemented: true,
		handler:     (*Server).handleUpdate,
	},
	{
		op:     OpListRelatedIssues,
		method: http.MethodGet,
		// A SUB-RESOURCE of the issue-detail path, and the surface's first. The
		// segment is a LITERAL after a single-segment wildcard, so pattern and
		// specPath agree and the router registers the documented path itself —
		// no declaration is needed and the claim row's exception does not apply.
		//
		// It collides with nothing. `/v0/beads/issues/{id}` is a different whole
		// path, which is what ServeMux matches on, and the custom-method
		// dispatcher's `/v0/beads/issues/{idop}` is registered under POST and is
		// one segment shorter besides.
		pattern:     "/v0/beads/issues/{id}/related",
		capability:  "issues.related",
		implemented: true,
		handler:     (*Server).handleListRelatedIssues,
	},
	{
		op:     OpAddComment,
		method: http.MethodPost,
		// The second SUB-RESOURCE row and the first that writes, spelled the way
		// the neighbor read above is: a LITERAL segment after a single-segment
		// wildcard, so pattern and specPath agree, the router registers the
		// documented path itself, and the claim row's exception does not apply.
		//
		// A PLAIN collection POST rather than a custom method, for the single
		// create's reason: this creates one member of the collection the path
		// names. It is the one place a sub-resource earns the method outright.
		//
		// It collides with nothing, and the POST wildcard is the collision worth
		// checking rather than assuming: `/v0/beads/issues/{idop}` matches ONE
		// segment after `/issues/`, and this path has two, so the dispatcher
		// never sees a request for it. TestCustomMethodsNarrowThePOSTSurface and
		// TestAddCommentPathReachesItsHandler pin the two halves.
		//
		// The collection has no GET row and must not grow one by reflex: no role
		// answers a comment page, and the thread is read through
		// `GET /v0/beads/issues/{id}?include_comments=true`. A GET here lands on
		// the catch-all, which answers 404 — this surface has no 405.
		pattern:     "/v0/beads/issues/{id}/comments",
		capability:  "issues.addComment",
		implemented: true,
		handler:     (*Server).handleAddComment,
	},
	{
		op:          OpListSettings,
		method:      http.MethodGet,
		pattern:     "/v0/beads/config",
		capability:  "config.list",
		implemented: true,
		handler:     (*Server).handleListSettings,
	},
	{
		op:          OpGetSetting,
		method:      http.MethodGet,
		pattern:     "/v0/beads/config/{key}",
		capability:  "config.get",
		implemented: true,
		handler:     (*Server).handleGetSetting,
	},
	{
		op:     OpSetSetting,
		method: http.MethodPut,
		// THE SURFACE'S FIRST PUT, and the method is what the operation means
		// rather than a preference: the caller names the resource by path and
		// sends the value that becomes its whole state, which is what PUT
		// already means, and the write is idempotent in the strict sense.
		//
		// It shares its pattern with the read above and the delete below and
		// differs only in method, which ServeMux registers together — the same
		// arrangement the memory key already has, so the three rows cannot
		// collide.
		pattern:     "/v0/beads/config/{key}",
		capability:  "config.set",
		implemented: true,
		handler:     (*Server).handleSetSetting,
	},
	{
		op:     OpUnsetSetting,
		method: http.MethodDelete,
		// The surface's second DELETE, and forgetMemory's argument unchanged:
		// one named resource, no body, no flags.
		pattern:     "/v0/beads/config/{key}",
		capability:  "config.unset",
		implemented: true,
		handler:     (*Server).handleUnsetSetting,
	},
	{
		op:          OpListDependencies,
		method:      http.MethodGet,
		pattern:     "/v0/beads/dependencies",
		capability:  "dependencies.list",
		implemented: true,
		handler:     (*Server).handleListDependencies,
	},
	{
		op:     OpCountDependencyEdges,
		method: http.MethodGet,
		// A collection-level custom method on the dependency collection,
		// spelled the way ready:count and issues:count are: both segments are
		// LITERAL, so pattern and specPath agree and the router registers the
		// documented path itself.
		//
		// It collides with nothing. The three literal paths under this
		// collection — /cycles, /blocking, /tree — all carry a separating
		// slash, and the plain collection GET is a different whole segment,
		// which is what ServeMux matches on.
		pattern:     "/v0/beads/dependencies:count",
		capability:  "dependencies.count",
		implemented: true,
		handler:     (*Server).handleCountDependencyEdges,
	},
	{
		op:     OpListBlockingAnnotations,
		method: http.MethodGet,
		// A literal path under the same collection as the stored-edge read. No
		// wildcard is involved, so it cannot collide with /v0/beads/dependencies
		// or /v0/beads/dependencies/cycles, both of which are literals too.
		pattern:     "/v0/beads/dependencies/blocking",
		capability:  "dependencies.blocking",
		implemented: true,
		handler:     (*Server).handleBlockingAnnotations,
	},
	{
		op:     OpGetDependencyTree,
		method: http.MethodGet,
		// A sibling path under /dependencies rather than a mode of the row
		// above: ServeMux matches the literal segment exactly, and the two
		// operations answer different shapes from different roles.
		pattern:     "/v0/beads/dependencies/tree",
		capability:  "dependencies.tree",
		implemented: true,
		handler:     (*Server).handleDependencyTree,
	},
	{
		op:     OpCreateIssue,
		method: http.MethodPost,
		// THE PLAIN COLLECTION POST, and the row the batch below deliberately
		// left this path free for: creating one member of the collection a path
		// names is what POST already means, so a single create needs no custom
		// method and squatting on the path with a batch would have made this
		// operation unnameable.
		//
		// It shares its pattern with no other row. The claim's wide
		// /v0/beads/issues/{idop} wildcard requires the separating slash this
		// path has none of, and every batch beside it is a literal `:verb`
		// segment ServeMux matches whole.
		pattern:     "/v0/beads/issues",
		capability:  "issues.create",
		implemented: true,
		handler:     (*Server).handleCreateIssue,
	},
	{
		op:     OpBatchCreateIssues,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way ready:count's is.
		//
		// It does not collide with the claim row's wide POST wildcard below.
		// That pattern is /v0/beads/issues/{idop} and requires the separating
		// slash; this path has none, so the two never match the same request.
		//
		// Nor with the single create above, which took the plain collection
		// POST this row was spelled as a custom method to leave free: ServeMux
		// prefers the literal `:batchCreate` segment, and the two paths differ
		// in any case.
		pattern:     "/v0/beads/issues:batchCreate",
		capability:  "issues.batchCreate",
		implemented: true,
		handler:     (*Server).handleBatchCreate,
	},
	{
		op:     OpApplyBatch,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way issues:batchCreate's
		// is, and preferred over the claim's wildcard for the reason the sweep
		// row below spells out: that pattern requires a separating slash, this
		// path has none, and ServeMux prefers the literal in any case.
		//
		// A SIBLING of issues:batchCreate rather than a mode of it. The two
		// answer different questions — one creates N issues, this one applies an
		// ordered plan of four verbs whose items may reference each other — and a
		// flag on that operation would have made one operationId carrying two
		// contracts, two request schemas and two result shapes.
		pattern:     "/v0/beads/issues:batchApply",
		capability:  "issues.batchApply",
		implemented: true,
		handler:     (*Server).handleApplyBatch,
	},
	{
		op:      OpClaimIssue,
		method:  http.MethodPost,
		pattern: customMethodPattern,
		// One of the rows where pattern and specPath differ. ServeMux wildcards
		// match a whole path segment, so `{id}:claim` is not expressible as a
		// pattern: the rows sharing this pattern register once and the
		// dispatcher splits the custom method off the matched segment
		// (customMethodTarget). Declaring specPath here keeps the parity test
		// honest instead of teaching it this exception; TestSpecRouteParity
		// bounds the exception's shape and TestClaimPathReachesItsHandler
		// drives the documented path.
		//
		// The wildcard therefore matches every POST under /v0/beads/issues/,
		// including the issue-detail path, which this document publishes under
		// GET and PATCH and under no other method. The dispatcher answers 404 —
		// the same answer the catch-all gives any other unrouted path — for a
		// segment that ends in no registered suffix, so the wide pattern stays a
		// routing detail rather than undocumented surface.
		// TestCustomMethodsNarrowThePOSTSurface pins it.
		//
		// That 404 needs no credential, because the split happens before
		// s.route: an unrouted suffix here is answered exactly as the catch-all
		// answers any other unrouted path, while a registered one reaches its
		// row and is refused. Paths are public spec, so the miss discloses
		// nothing the document does not already publish;
		// TestUnroutedPathsStayUnauthenticated pins both halves so neither gets
		// "fixed" into the other.
		specPath:     "/v0/beads/issues/{id}:claim",
		customMethod: ":claim",
		capability:   "issues.claim",
		implemented:  true,
		handler:      (*Server).handleClaim,
	},
	{
		op:     OpReleaseIssue,
		method: http.MethodPost,
		// The claim's inverse, on the dispatcher the close built. A fifth row on
		// this pattern is a row; everything the claim row says about the
		// wildcard's width holds here unchanged.
		pattern:      customMethodPattern,
		specPath:     "/v0/beads/issues/{id}:release",
		customMethod: ":release",
		capability:   "issues.release",
		implemented:  true,
		handler:      (*Server).handleRelease,
	},
	{
		op:     OpCloseIssue,
		method: http.MethodPost,
		// The second row on the shared wildcard, and the reason the dispatcher
		// exists: two rows cannot each register this pattern. Everything the
		// claim row says about the wildcard's width holds here unchanged.
		pattern:      customMethodPattern,
		specPath:     "/v0/beads/issues/{id}:close",
		customMethod: ":close",
		capability:   "issues.close",
		implemented:  true,
		handler:      (*Server).handleClose,
	},
	{
		op:     OpCompareAndSetMetadata,
		method: http.MethodPost,
		// A fourth row on the shared single-resource dispatcher, spelled the way
		// the reopen's is.
		pattern:      customMethodPattern,
		specPath:     "/v0/beads/issues/{id}:casMetadata",
		customMethod: ":casMetadata",
		capability:   "issues.casMetadata",
		implemented:  true,
		handler:      (*Server).handleCompareAndSetMetadata,
	},
	{
		op:     OpReopenIssue,
		method: http.MethodPost,
		// The close's mirror, on the dispatcher the close built. Nothing new is
		// needed here: a third row on this pattern is a row.
		pattern:      customMethodPattern,
		specPath:     "/v0/beads/issues/{id}:reopen",
		customMethod: ":reopen",
		capability:   "issues.reopen",
		implemented:  true,
		handler:      (*Server).handleReopen,
	},
	{
		op:     OpBatchCloseIssues,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way issues:batchCreate
		// is — and this operation is that one's deliberate opposite: it is not
		// all-or-nothing, so its 200 carries per-item refusals.
		pattern:     "/v0/beads/issues:batchClose",
		capability:  "issues.batchClose",
		implemented: true,
		handler:     (*Server).handleBatchClose,
	},
	{
		op:     OpClaimNextIssue,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way issues:sweep is,
		// and preferred over the claim's wildcard for that row's reason: the
		// segment is a LITERAL, so the router registers the documented path
		// itself and ServeMux prefers it over the wildcard for this exact path.
		//
		// It names no id BECAUSE IT NAMES NO ROW. The caller asks a question and
		// the role picks the answer, which is what makes this a sibling of
		// issues/{id}:claim rather than a mode of it.
		pattern:     "/v0/beads/issues:claimNext",
		capability:  "issues.claimNext",
		implemented: true,
		handler:     (*Server).handleClaimNext,
	},
	{
		op:     OpSweepIssues,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way countReadyWork's is.
		//
		// It is registered AFTER the claim's `/v0/beads/issues/{idop}`, and
		// ServeMux precedence is by specificity rather than by order, so the
		// literal wins over the wildcard for this exact path. That is what
		// keeps a sweep from being parsed as a claim of an issue called
		// ":sweep"; TestSweepPathReachesItsHandler drives the documented path.
		pattern:     "/v0/beads/issues:sweep",
		capability:  "issues.sweep",
		implemented: true,
		handler:     (*Server).handleSweep,
	},
	{
		op:     OpDeleteIssues,
		method: http.MethodPost,
		// A literal collection-level custom method, registered and preferred
		// over the claim's wildcard for exactly the reason the sweep row above
		// spells out; TestDeletePathReachesItsHandler drives the documented
		// path.
		pattern:     "/v0/beads/issues:delete",
		capability:  "issues.delete",
		implemented: true,
		handler:     (*Server).handleDelete,
	},
	{
		op:     OpAddDependencies,
		method: http.MethodPost,
		// A collection-level custom method beside :remove below, and a LITERAL
		// for the same reason: pattern and specPath agree, so no declaration is
		// needed and the router registers the documented path itself.
		pattern:     "/v0/beads/dependencies:add",
		capability:  "dependencies.add",
		implemented: true,
		handler:     (*Server).handleAddDependencies,
	},
	{
		op:     OpRemoveDependency,
		method: http.MethodPost,
		// A collection-level custom method on the dependency collection,
		// spelled the way ready:count's is. Both segments are LITERAL, so
		// pattern and specPath agree and the router registers the documented
		// path itself — no wildcard is involved and nothing here needs the
		// claim row's declaration.
		//
		// It cannot collide with the three literal paths UNDER
		// /v0/beads/dependencies (cycles, blocking, tree): ServeMux requires the
		// separating slash, and this path has none.
		pattern:     "/v0/beads/dependencies:remove",
		capability:  "dependencies.remove",
		implemented: true,
		handler:     (*Server).handleRemoveDependency,
	},
	{
		op:          OpListMemories,
		method:      http.MethodGet,
		pattern:     "/v0/beads/memories",
		capability:  "memories.list",
		implemented: true,
		handler:     (*Server).handleListMemories,
	},
	{
		op:     OpRememberMemory,
		method: http.MethodPost,
		// A plain collection POST, not a custom method: this creates one member
		// of the collection the path names, which is what POST already means.
		// The two destructive issue operations above are custom methods because
		// they act on a SET the request describes; nothing here does.
		pattern:     "/v0/beads/memories",
		capability:  "memories.remember",
		implemented: true,
		handler:     (*Server).handleRememberMemory,
	},
	{
		op:     OpGetMemory,
		method: http.MethodGet,
		// A single-segment wildcard, so pattern and specPath agree and no
		// declaration is needed. It cannot collide with the collection read
		// above: ServeMux requires the separating slash, so `/memories` and
		// `/memories/{key}` never match the same request.
		pattern:     "/v0/beads/memories/{key}",
		capability:  "memories.get",
		implemented: true,
		handler:     (*Server).handleGetMemory,
	},
	{
		op:     OpListEvents,
		method: http.MethodGet,
		// A plain collection read, not a custom method and not a sub-resource of
		// issues: the journal is its own collection whose members happen to
		// describe issue mutations. `since` and `limit` are ordinary query
		// parameters, exactly as they are on /v0/beads/issues, so pattern and
		// specPath agree and no declaration is needed.
		pattern:     "/v0/beads/events",
		capability:  "events.list",
		implemented: true,
		handler:     (*Server).handleListEvents,
	},
	{
		op:     OpWatchEvents,
		method: http.MethodGet,
		// A collection-level custom method on the journal, spelled the way
		// ready:count is: both segments are LITERAL, so pattern and specPath
		// agree and the router registers the documented path itself.
		//
		// It cannot collide with the paged read above — ServeMux matches the
		// whole path and these two differ — and it is a SIBLING of it rather
		// than a mode of it deliberately: the two answer different media types
		// with different lifetimes and different limits, and a `follow=true`
		// parameter would have made one operation that is two contracts.
		pattern:    "/v0/beads/events:watch",
		capability: "events.watch",
		// The stream lives until the client leaves, so the request deadline
		// does not apply and the database slot moves to the individual reads.
		// See the field comments above; readWatchBatch is the other half.
		streaming:       true,
		bypassSemaphore: true,
		implemented:     true,
		handler:         (*Server).handleWatchEvents,
	},
	{
		op:     OpForgetMemory,
		method: http.MethodDelete,
		// The surface's first DELETE. It shares a pattern with the read above
		// and differs only in method, which is the whole argument for the
		// method: one named resource, no body, no flags. ServeMux registers
		// method and pattern together, so the two rows cannot collide.
		pattern:     "/v0/beads/memories/{key}",
		capability:  "memories.forget",
		implemented: true,
		handler:     (*Server).handleForgetMemory,
	},
}

// specPathOf is the document path for a route, defaulting to the router
// pattern for the rows where they agree.
func (r route) specPathOf() string {
	if r.specPath != "" {
		return r.specPath
	}
	return r.pattern
}

// behaviorCapabilities are advertised tokens that name a server-wide BEHAVIOR
// rather than an operation. They ride in the same ContextResponse.capabilities
// list as the per-operation tokens — a client checks the one list — but they are
// not derived from the route table, because the behavior they announce is not a
// route. project.enforce announces per-request Bd-Project-Id enforcement
// (checkProjectStamp): a stamped client reads it to know the refusal is available
// rather than silently dropped by an older server.
var behaviorCapabilities = []string{CapProjectEnforce}

// Capabilities lists what this build advertises in ContextResponse.capabilities:
// the operations it actually implements, gated on `implemented` so a stub can
// never advertise itself, PLUS the behavior tokens for server-wide behaviors this
// build enforces. A client that checks capabilities before calling gets a
// truthful answer from every release, including one cut halfway through the
// endpoint slices.
func Capabilities() []string {
	var out []string
	for _, rt := range routeTable {
		if rt.implemented && rt.capability != "" {
			out = append(out, rt.capability)
		}
	}
	out = append(out, behaviorCapabilities...)
	slices.Sort(out)
	return out
}
