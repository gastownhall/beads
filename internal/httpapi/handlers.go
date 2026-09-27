package httpapi

import (
	"maps"
	"net/http"
	"slices"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// handleHealth answers from the process and touches nothing that can fail.
//
// This is LIVENESS, not readiness: it stays 200 while the database is
// unreachable, wedged, or an hour dead. The documented readiness probe is
// GET /v0/beads/ready?limit=1, which goes through the semaphore and a real
// transaction.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	writeJSON(w, apigen.Health{Status: apigen.Ok})
}

// handleContext answers the identity handshake from the startup snapshot.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	writeJSON(w, s.ctxBody)
}

// requireNoQuery enforces the document's unknown-parameter rule for the
// operations that take no parameters at all. It reports whether the request may
// proceed.
//
// Silently ignoring an unrecognized parameter is the failure this exists to
// prevent: on a filtering operation it WIDENS the result set, so a client one
// version ahead receives rows it believes it filtered out. Rejecting is also a
// client's only per-parameter capability probe, since `capabilities` is
// operation-level.
func (s *Server) requireNoQuery(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	if len(q) == 0 {
		return true
	}
	// Name one key, and always the same one for the same request: `param` is
	// what the client dispatches on, so it must not depend on map order.
	offender := slices.Min(slices.Collect(maps.Keys(q)))
	res := InvalidArgument(offender, ReasonUnknownParameter,
		"this operation takes no query parameters")
	if offender == "" {
		// The degenerate `?=1` spelling parses to a parameter whose name is the
		// empty string. InvalidArgument reads "" as "this input has no nameable
		// part" and omits `param` — true of an unparseable body, not of this —
		// and the document promises `param` on every other 400. Name it.
		res.Problem.Param = &offender
	}
	requestInfo(r.Context()).refuse(offender)
	s.fail(w, r, res)
	return false
}

// contextResponse projects the workspace snapshot onto the wire.
//
// The snapshot's own fields come from domain.PublishedContext, which is the
// projection `bd context` publishes too — so the identity this server hands an
// automation client and the identity the CLI prints beside it are the same
// seven values read the same way, not two field-by-field copies kept in sync
// by hand.
//
// That projection is also where the EXCLUSIONS live, and living there is what
// makes them structural rather than remembered. domain.PublishedContextFields
// has no SyncRemote member at all — it is populated unconditionally from the
// workspace's sync.remote config and remote URLs routinely embed credentials
// — so this handler cannot publish one by forgetting to leave it out. Nor can
// it publish the database bind endpoint (advertising it invites clients to
// bypass this API and dial a server whose trust model is "root with an empty
// password on loopback") or the absolute host paths. BeadsDir and RepoRoot ARE
// published, deliberately: they are the most legible workspace identity for
// the operator reading a single-workspace server, and the document says so.
//
// Those two are also the response's only OPTIONAL members, and this server
// always publishes them anyway. The optionality is the document's promise to
// CLIENTS — that absence is legal and must be tolerated — not a switch here.
//
// The three members that are NOT the workspace's are named here, because they
// are this surface's own: the API version, the schema version, and the
// capability set derived from the implemented handlers.
func contextResponse(info domain.ContextInfo, schemaVersion int, capabilities []string) apigen.ContextResponse {
	if capabilities == nil {
		// The document types this as an array; a client must never have to
		// tell null from empty to learn that nothing is implemented.
		capabilities = []string{}
	}
	published := domain.PublishedContext(info)
	return apigen.ContextResponse{
		ApiVersion:    APIVersion,
		SchemaVersion: schemaVersion,
		Capabilities:  capabilities,

		BdVersion: published.BdVersion,
		Backend:   published.Backend,
		DoltMode:  published.DoltMode,
		Database:  published.Database,
		BeadsDir:  &published.BeadsDir,
		RepoRoot:  &published.RepoRoot,
		ProjectId: published.ProjectID,
	}
}
