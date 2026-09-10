package httpapi

import (
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// handleDependencyCycles answers GET /v0/beads/dependencies/cycles.
//
// WHAT IS NOT HERE, as for the read operations next door: no graph is built, no
// table is chosen, no path is rotated and no report is sorted. The
// canonicalization the document promises is inside issueops.CycleDetector's
// implementation, which `bd dep cycles` reaches through the same accessor; a
// handler that ordered the answer for itself would be a second definition of
// "canonical".
//
// The element type is an ALIAS of issueops.Cycle — the same struct the CLI's
// --json marshals — so there is no second wire struct here and there must never
// be one.
func (s *Server) handleDependencyCycles(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}

	detector, err := s.cycleDetector(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	report, err := detector.DetectCycles(r.Context(), issueops.DetectCyclesRequest{})
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	writeJSON(w, apigen.CyclesPage{
		Items: wireCycles(report.Cycles),
		// Always false: this operation takes no limit, so nothing truncates the
		// report. It is emitted rather than omitted because the document types
		// it as required, and because a client must not have to tell "not
		// truncated" from "this server does not say".
		HasMore: false,
	})
}

// wireCycles projects the role's report onto the generated envelope's element
// type, which is an alias of the role's own struct.
//
// It exists for the one thing that is not free: the document says `items` is an
// empty array and never null. Making the guarantee here as well means the wire
// promise does not depend on the role keeping its own.
func wireCycles(cycles []issueops.Cycle) []apigen.Cycle {
	if cycles == nil {
		return []apigen.Cycle{}
	}
	return cycles
}
