package bdpwire

import (
	"encoding/json"
)

// Reference is the bundle's `reference` sum: how anything in BDP points at
// anything. On the wire it is either an absolute URI string, or a pinned
// reference — an object carrying the URI plus the revision the reference was
// made against. The URI is always the complete identity; whether it is
// in-Scope or out-of-Scope is derived from it, never declared. The pin is
// recorded provenance: stored and echoed byte-identically, compared only for
// equality, never validated in v0, and it adds no identity component.
//
// The Go value collapses the two arms into one struct because the bundle
// makes them disjoint: a pinned reference's revision has minLength 1, so
// Revision == "" is the string arm and anything else is the object arm, with
// nothing lost either way. DECISION: the decoder enforces exactly that
// disjointness — an object arm with a missing or empty revision is rejected,
// because the Go value would otherwise read as the string arm — and nothing
// more: an empty URI decodes (either arm can carry it faithfully; a validator
// rejects it), URI grammar is a model law rather than a wire shape, and the
// realization fixtures legitimately carry local-ID spellings that an
// authority canonicalizes.
type Reference struct {
	URI      string
	Revision string
}

// Pinned reports whether the reference carries a revision.
func (r Reference) Pinned() bool {
	return r.Revision != ""
}

// pinnedReferenceJSON is the `pinnedReference` envelope, the object arm of
// Reference on the wire. Closed: exactly these two members.
type pinnedReferenceJSON struct {
	URI      string `json:"uri"`
	Revision string `json:"revision"`
}

// MarshalJSON writes the string arm for an unpinned reference and the object
// arm for a pinned one. It is total: a zero Reference marshals as "", which
// is the shape of a reference with nothing in it, and a validator's problem.
func (r Reference) MarshalJSON() ([]byte, error) {
	if !r.Pinned() {
		return json.Marshal(r.URI)
	}
	return json.Marshal(pinnedReferenceJSON{URI: r.URI, Revision: r.Revision})
}

// UnmarshalJSON accepts the two arms of the sum and nothing else — not null,
// not a number, not an object with members beyond uri and revision (the
// bundle closes pinnedReference; names are exact, so "URI" is a stranger
// and a duplicate is refused), not an object arm whose uri is null or whose
// revision is absent, null or empty. It is decodeReference (decode.go), so a
// Reference decoded through encoding/json is held to the same rule as one
// nested in a record decoded through Unmarshal.
func (r *Reference) UnmarshalJSON(data []byte) error {
	return decodeReference(data, r, "reference")
}
