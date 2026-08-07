package httpapi

import (
	"net/http"
	"slices"
)

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
	// claim route and a derivation would have to encode that exception.
	specPath string
	// capability is the token this operation contributes to
	// ContextResponse.capabilities, or "" for operations outside that
	// vocabulary. A stub contributes nothing whatever this says.
	capability string
	// bypassSemaphore exempts an operation from the database slot limit. Only
	// legitimate for handlers that touch no database: liveness and identity
	// must stay answerable while every slot is held by a long scan.
	bypassSemaphore bool
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
		implemented:     true,
		handler:         (*Server).handleHealth,
	},
	{
		op:      OpGetContext,
		method:  http.MethodGet,
		pattern: "/v0/beads/context",
		// A startup snapshot, so it touches no database either — and identity
		// staying observable under saturation is half of how an operator
		// tells one wedged server from another.
		bypassSemaphore: true,
		implemented:     true,
		handler:         (*Server).handleContext,
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
		op:          OpGetIssue,
		method:      http.MethodGet,
		pattern:     "/v0/beads/issues/{id}",
		capability:  "issues.get",
		implemented: true,
		handler:     (*Server).handleGetIssue,
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
		op:          OpListDependencies,
		method:      http.MethodGet,
		pattern:     "/v0/beads/dependencies",
		capability:  "dependencies.list",
		implemented: true,
		handler:     (*Server).handleListDependencies,
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
		op:     OpBatchCreateIssues,
		method: http.MethodPost,
		// A collection-level custom method, spelled the way ready:count's is.
		//
		// It does not collide with the claim row's wide POST wildcard below.
		// That pattern is /v0/beads/issues/{idop} and requires the separating
		// slash; this path has none, so the two never match the same request.
		//
		// It also leaves POST /v0/beads/issues free. A collection POST is where
		// a single create belongs when one is published, and squatting on it
		// with a batch would have made that operation unnameable.
		pattern:     "/v0/beads/issues:batchCreate",
		capability:  "issues.batchCreate",
		implemented: true,
		handler:     (*Server).handleBatchCreate,
	},
	{
		op:      OpClaimIssue,
		method:  http.MethodPost,
		pattern: "/v0/beads/issues/{" + claimPathValue + "}",
		// The one row where pattern and specPath differ. ServeMux wildcards
		// match a whole path segment, so `{id}:claim` is not expressible as a
		// pattern: the handler takes the segment whole and splits the custom
		// method off itself (claimTarget). Declaring specPath here keeps the
		// parity test honest instead of teaching it this exception;
		// TestSpecRouteParity bounds the exception's shape and
		// TestClaimPathReachesItsHandler drives the documented path.
		//
		// The wildcard therefore matches every POST under /v0/beads/issues/,
		// including the issue-detail path the document declares GET-only.
		// claimTarget answers 404 — the same answer the catch-all gives any
		// other unrouted path — for a segment that does not end in the custom
		// method, so the wide pattern stays a routing detail rather than
		// undocumented surface. TestClaimNarrowsThePOSTSurface pins it.
		specPath:    "/v0/beads/issues/{id}:claim",
		capability:  "issues.claim",
		implemented: true,
		handler:     (*Server).handleClaim,
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

// Capabilities lists the operations this build actually implements, which is
// what ContextResponse.capabilities carries. Derived from the route table and
// gated on `implemented`, so a stub can never advertise itself: a client that
// checks capabilities before calling gets a truthful answer from every release,
// including one cut halfway through the endpoint slices.
func Capabilities() []string {
	var out []string
	for _, rt := range routeTable {
		if rt.implemented && rt.capability != "" {
			out = append(out, rt.capability)
		}
	}
	slices.Sort(out)
	return out
}
