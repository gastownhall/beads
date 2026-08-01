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
		op:          OpListIssues,
		method:      http.MethodGet,
		pattern:     "/v0/beads/issues",
		capability:  "issues.list",
		implemented: true,
		handler:     (*Server).handleListIssues,
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
