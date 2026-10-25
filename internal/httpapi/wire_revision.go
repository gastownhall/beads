package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// WireRevisionHeader names the optional per-request wire-floor declaration. A
// client that knows the revision it was compiled against puts that revision
// here; checkWireRevision refuses the request when it names a revision below
// MinClientWireRevision, before the client can act on a body shaped for a
// revision it has already said it cannot decode. The header is optional and
// absent by default: an older client that never sends it is served exactly as
// before, which is what keeps this additive rather than a new precondition.
const WireRevisionHeader = "Bd-Wire-Revision"

// CurrentWireRevision is ContextResponse.wire_revision: the wire-shape counter
// for THIS build. See the `wire_revision` property in openapi.v0.yaml for the
// full revision history and what bumps it. It increases only alongside a
// deliberate, non-additive wire change AND a matching update to the
// internal/httpapi/wireshape golden digest — TestWireShapeDigest fails closed
// the moment the two disagree.
const CurrentWireRevision = 2

// MinClientWireRevision is ContextResponse.min_client_wire_revision: the
// oldest client-declared revision this build still answers correctly. Raising
// it is the deliberate act of dropping support for clients built against an
// older wire shape; it must never exceed CurrentWireRevision.
const MinClientWireRevision = 0

// checkWireRevision enforces the optional per-request wire-floor declaration.
// A client that knows the revision it was compiled against may assert it in
// the Bd-Wire-Revision header; if that revision is below s.minClientWireRevision,
// the request is refused before the server spends any work building a body the
// caller has already said it cannot decode. It returns nil when the request
// may proceed, or the 400 to write.
//
// s.minClientWireRevision starts at the package constant MinClientWireRevision
// and is a Server field — rather than a direct read of that constant — only so
// a test can raise the floor; production never overrides it.
//
// Three paths return nil. An ABSENT header is the backward-compatible one: an
// older client never sends it, so enforcement triggers only when the header
// arrives and adds no precondition to any request already in the field. A
// non-negative integer at or above s.minClientWireRevision is in range. A value
// that fails to parse as a non-negative integer is NOT this check's refusal —
// it is a malformed header, reported as `invalid_value` rather than
// `wire_revision_unsupported`, which is reserved for a revision this server
// has read and understood to be too old.
//
// The refusal is recorded on the request line like every other middleware
// refusal, so a client persistently declaring a dropped revision is
// attributable down to the local process.
func (s *Server) checkWireRevision(r *http.Request) *Result {
	raw := r.Header.Get(WireRevisionHeader)
	if raw == "" {
		return nil
	}
	got, ok := parseStrictNonNegativeInt(raw)
	if !ok {
		requestInfo(r.Context()).refuse(raw)
		res := InvalidArgument(WireRevisionHeader, ReasonInvalidValue,
			"the "+WireRevisionHeader+" header must be a non-negative integer")
		return &res
	}
	if got >= s.minClientWireRevision {
		return nil
	}
	requestInfo(r.Context()).refuse(raw)
	res := WireRevisionUnsupported(got, s.minClientWireRevision, CurrentWireRevision, s.ctxBody.BdVersion)
	return &res
}

// parseStrictNonNegativeInt accepts exactly the plain-decimal shape this
// header is documented to carry: one or more ASCII digits, no sign, and no
// leading zero unless the whole value is the single digit "0". strconv.Atoi
// alone is looser than that — it also accepts a leading "+", and it accepts
// leading zeros like "007" — both of which would let two different header
// bytes name the same revision, which is a wire ambiguity this header exists
// to avoid, not a client convenience to extend to it.
func parseStrictNonNegativeInt(raw string) (int, bool) {
	if raw == "" || strings.ContainsAny(raw, "+-") {
		return 0, false
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	got, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return got, true
}
