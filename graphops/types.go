package graphops

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The domain values. Every one of them has unexported fields and a constructor
// that enforces the laws in laws.go, so a caller holding a value holds a valid
// one and an implementation never re-validates what it is handed. Values are
// immutable: accessors return copies of anything a caller could otherwise
// write through.
//
// EVERY STRING IS VALID UTF-8. A revision, a pin, a principal, a descriptor's
// name, description or label — every opaque string a value carries — ends up
// in a JSON document: a wire record, a canonical descriptor, a hashed ledger
// event. encoding/json rewrites an invalid byte to U+FFFD on the way out, so
// two Go strings that differ only in invalid bytes ("\xff" and "\xfe") would
// be two distinct domain values with one serialization — and, in the ledger,
// one hash. The constructors therefore refuse invalid UTF-8 outright. This is
// representation validation, not interpretation: an opaque token's meaning
// is still nobody's business here (P0 council, 2026-09-07).

// validUTF8 refuses a string that is not valid UTF-8, naming the member.
func validUTF8(s, what string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s must be valid UTF-8", ErrValidation, what)
	}
	return nil
}

// ResourceKind is the closed set of Resource categories: a Type Descriptor
// describes exactly one, a ledger event names one, and a canonical path
// belongs to one by its fixed root.
type ResourceKind string

// The two Resource kinds. There is no third, and no synthetic root Type.
const (
	KindBead ResourceKind = "bead"
	KindLink ResourceKind = "link"
)

// Valid reports whether k is one of the two kinds.
func (k ResourceKind) Valid() bool { return k == KindBead || k == KindLink }

// AllocationState is the state of one committed canonical path in the
// allocation ledger: live, reserved (ledger-applied with no row behind it),
// pruned, or erased. It is a string alias, as the design spells it, so the
// storage leg's ENUM column and the GoneError carry the same value.
type AllocationState = string

// The allocation states. A path leaves "live" and never returns; a path is
// never reused whatever its state.
const (
	AllocationLive     AllocationState = "live"
	AllocationReserved AllocationState = "reserved"
	AllocationPruned   AllocationState = "pruned"
	AllocationErased   AllocationState = "erased"
)

// Cursor is an OPAQUE continuation produced by a store and handed back to it
// unchanged. It binds whatever the store needs — Scope URL, epoch, selection
// hash, last path, and from P2 a snapshot identity — inside a value no caller
// inspects. There is no law over its spelling here: a cursor a store did not
// mint is a cursor that store refuses.
type Cursor string

// Direction selects which incident Links of a Bead a read returns. The zero
// value is DirectionBoth, which is also BDP's default when the parameter is
// omitted, so a zero IncidentRequest asks the protocol's default question.
type Direction uint8

// The three directions.
const (
	// DirectionBoth selects the union of inbound and outbound Links.
	DirectionBoth Direction = iota
	// DirectionIn selects Links whose target is the Bead.
	DirectionIn
	// DirectionOut selects Links whose source is the Bead.
	DirectionOut
)

// Valid reports whether d is one of the three directions.
func (d Direction) Valid() bool { return d <= DirectionOut }

// String spells the direction for a human; the wire spelling belongs to the
// handler.
func (d Direction) String() string {
	switch d {
	case DirectionBoth:
		return "both"
	case DirectionIn:
		return "in"
	case DirectionOut:
		return "out"
	}
	return fmt.Sprintf("Direction(%d)", uint8(d))
}

// MintOpaqueToken returns 128 bits from crypto/rand as 32 lowercase hex
// digits: the shape of every revision this authority mints, of every operation
// id, and of every authority id (the CHAR(32) columns of the schema). It never
// fails — crypto/rand.Read in this Go release does not return an error.
func MintOpaqueToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Revision names one state of one Bead or Link. It is OPAQUE and EQUALITY-ONLY:
// a client compares revisions and derives nothing from their spelling. This
// authority mints them with MintRevision (128 random bits, lower-hex); a
// revision read from another authority through the wire client is whatever
// nonempty string that authority chose, carried byte-identically.
//
// DECISION: the value admits any nonempty string, not only the 32-hex form
// this store mints. The design gives the minting rule; the pinned spec gives
// the value rule (opaque, nonempty, equality-only), and a graphops.Reader over
// the wire must represent a foreign authority's revisions faithfully.
type Revision struct{ token string }

// MintRevision returns a fresh revision in this authority's format.
func MintRevision() Revision { return Revision{token: MintOpaqueToken()} }

// NewRevision admits an opaque revision token. Emptiness and invalid UTF-8
// are refused (the token is carried in JSON and hashed in the ledger, see
// the package's UTF-8 rule); a revision has no other law.
func NewRevision(token string) (Revision, error) {
	if token == "" {
		return Revision{}, fmt.Errorf("%w: a revision must be a nonempty opaque string", ErrValidation)
	}
	if err := validUTF8(token, "a revision"); err != nil {
		return Revision{}, err
	}
	return Revision{token: token}, nil
}

// String returns the token.
func (r Revision) String() string { return r.token }

// IsZero reports whether r carries no revision.
func (r Revision) IsZero() bool { return r.token == "" }

// Equal is the only comparison a revision supports.
func (r Revision) Equal(o Revision) bool { return r.token == o.token }

// AttributionStatus is the closed set of bases for a carried attribution.
type AttributionStatus string

// The two statuses. There is deliberately no status that asserts
// authentication: the member is data, not evidence.
const (
	// AttributionClaimed: the principal was supplied by the writer of that
	// version, as written.
	AttributionClaimed AttributionStatus = "claimed"
	// AttributionUnknown: the principal is carried from data whose relation to
	// this version the realization cannot establish (an import; a creator
	// recorded where the writer of the current version was not).
	AttributionUnknown AttributionStatus = "unknown"
)

// Valid reports whether s is one of the two statuses.
func (s AttributionStatus) Valid() bool {
	return s == AttributionClaimed || s == AttributionUnknown
}

// Attribution is the carried, per-version attribution of a Bead or Link:
// data the protocol transports and attests nothing about. The zero value means
// ABSENT — no attribution was recorded for the version — which is why Bead and
// Link accessors return it with a presence flag.
type Attribution struct {
	principal string
	status    AttributionStatus
}

// NewAttribution builds a present attribution: a nonempty opaque principal and
// one of the two statuses. Principals are compared for byte equality only;
// BDP mandates no namespace.
func NewAttribution(principal string, status AttributionStatus) (Attribution, error) {
	if principal == "" {
		return Attribution{}, fmt.Errorf("%w: attribution principal must be nonempty", ErrValidation)
	}
	if err := validUTF8(principal, "attribution principal"); err != nil {
		return Attribution{}, err
	}
	if !status.Valid() {
		return Attribution{}, fmt.Errorf("%w: attribution status %q is not claimed or unknown", ErrValidation, status)
	}
	return Attribution{principal: principal, status: status}, nil
}

// Principal names who the version is attributed to.
func (a Attribution) Principal() string { return a.principal }

// Status is the realization's basis for the value.
func (a Attribution) Status() AttributionStatus { return a.status }

// IsZero reports the absent attribution.
func (a Attribution) IsZero() bool { return a.principal == "" }

// Properties is the authored JSON OBJECT of a Bead or Link, held as ONE
// canonical byte string: the RFC 8785 form of the document with numbers kept
// exact and admitted only when they round-trip through binary64 (see
// CanonicalizeJSON; bdp#21). Two Properties are the same value exactly when
// their bytes are equal, and that equality is the RFC 6902 §4.6 comparison the
// no-op law is made of — so a write whose result Equal()s the value before it
// mints no revision.
//
// The zero value is the empty object. Properties is never engine JSON: the
// storage leg stores these bytes in a BLOB and serves them back unchanged, so
// what a creator wrote — 9007199254740992, 1e300, 0.1 — survives exactly as
// canonicalized, never as some engine's float64 reading of it; and a number
// no binary64 reader could hand back unchanged (9007199254740993) is refused
// at the door rather than stored and later rounded by a peer.
type Properties struct{ canonical []byte }

var emptyObject = []byte("{}")

// NewProperties admits a JSON document as a properties object. The document
// must be a single JSON object; duplicate keys, invalid UTF-8, lone surrogate
// escapes, and anything after the closing brace are refused (ErrValidation).
// The bytes are canonicalized and copied; the caller's slice is not retained.
func NewProperties(raw []byte) (Properties, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return Properties{}, fmt.Errorf("%w: properties: %s", ErrValidation, reason(err))
	}
	if canonical[0] != '{' {
		return Properties{}, fmt.Errorf("%w: properties must be a JSON object", ErrValidation)
	}
	if bytes.Equal(canonical, emptyObject) {
		return Properties{}, nil
	}
	return Properties{canonical: canonical}, nil
}

// Bytes returns a copy of the canonical bytes; "{}" for the zero value.
func (p Properties) Bytes() []byte {
	if p.canonical == nil {
		return append([]byte(nil), emptyObject...)
	}
	return append([]byte(nil), p.canonical...)
}

// String returns the canonical bytes as a string.
func (p Properties) String() string {
	if p.canonical == nil {
		return "{}"
	}
	return string(p.canonical)
}

// Equal is RFC 6902 §4.6 value equality: numerically equal numbers, code-point
// equal strings, member-set equal objects, element-wise equal arrays.
func (p Properties) Equal(q Properties) bool {
	return bytes.Equal(p.Bytes(), q.Bytes())
}

// IsEmpty reports the empty object.
func (p Properties) IsEmpty() bool { return p.canonical == nil }

// MarshalJSON emits the canonical bytes. NOTE: encoding/json's Marshal
// post-processes a Marshaler's output and, by default, rewrites the literal
// characters '<', '>' and '&' as <, > and &; an encoder with
// SetEscapeHTML(false) — or a caller using Bytes() directly — gets the
// canonical form.
func (p Properties) MarshalJSON() ([]byte, error) { return p.Bytes(), nil }

// UnmarshalJSON admits a document through NewProperties.
func (p *Properties) UnmarshalJSON(raw []byte) error {
	q, err := NewProperties(raw)
	if err != nil {
		return err
	}
	*p = q
	return nil
}

// Ref is a Reference: how anything in BDP points at anything. It is a SUM, not
// a naked pair — an in-Scope reference (a canonical Bead path, rendered against
// the live Scope URL at the boundary) or an external one (an absolute URI
// outside the Scope, preserved byte-identically and never dereferenced) — and
// either may carry a pin: the revision the reference was made against, stored
// and echoed byte-identically, compared only for equality, never validated.
//
// Reference identity is the URI alone. Equality of endpoints, incident
// traversal and multiplicity ignore the pin; SameURI is that law, Equal is
// whole-value equality including the pin.
//
// The zero value is not a reference; constructors are the only way in.
type Ref struct {
	inScope bool
	path    string // in-Scope: canonical Bead path
	uri     string // external: the absolute URI, byte-identical
	pin     string // "" when unpinned
}

// NewInScopeRef builds a reference to a Bead of this Scope by its canonical
// path. DECISION: an in-Scope endpoint names a Bead — the pinned spec's
// endpoint rule ("MUST identify a live Bead in the Link's Scope") — so a Link
// path is refused here; a reference to a Link has no in-Scope form in v0.
func NewInScopeRef(path, pin string) (Ref, error) {
	if err := ValidateBeadPath(path); err != nil {
		return Ref{}, err
	}
	if err := validUTF8(pin, "a reference pin"); err != nil {
		return Ref{}, err
	}
	return Ref{inScope: true, path: path, pin: pin}, nil
}

// ParseRef admits a reference as written — a canonical local Bead ID
// ("beads/…"), the Bead's absolute canonical URL under scopeURL, or an
// absolute URI outside the Scope — and classifies it by the pinned law: a URI
// that resolves to (an alias of) the canonical Scope URL claims an in-Scope
// Bead and is refused unless it IS that Bead's canonical spelling; every
// other absolute URI is an opaque external reference, kept byte-identically.
//
// What "resolves to an alias of the Scope URL" means here is the WHATWG
// origin the URL parser would serialize (laws.go, normalizeOrigin: scheme
// and host case, every IPv4 and IPv6 spelling of the host, percent-encoded
// host characters, a default or zero-padded port) plus RFC 3986 §6.2.2
// path normalization (dot segments, percent-encoding case and
// unreserved-character escapes); a different origin is external. A local
// spelling under links/ or alias/ is refused — endpoints are Beads, and
// alias resolution is not served in v0 (DECISION: no alias table exists; an
// alias URL cannot be resolved at admission and is refused rather than
// stored). scopeURL is held to ValidateScopeURL, the client rule: a
// reference into a development server's local-test Scope is admissible.
func ParseRef(scopeURL, reference, pin string) (Ref, error) {
	if err := ValidateScopeURL(scopeURL); err != nil {
		return Ref{}, err
	}
	if reference == "" {
		return Ref{}, fmt.Errorf("%w: reference must be a canonical local Bead ID or an absolute URI", ErrValidation)
	}
	if err := validUTF8(pin, "a reference pin"); err != nil {
		return Ref{}, err
	}
	switch {
	case strings.HasPrefix(reference, "beads/"):
		return NewInScopeRef(reference, pin)
	case strings.HasPrefix(reference, "links/"), strings.HasPrefix(reference, "alias/"):
		return Ref{}, fmt.Errorf("%w: reference %q: an in-Scope endpoint must be a Bead's canonical ID", ErrValidation, reference)
	}
	if !isAbsoluteURI(reference) {
		return Ref{}, fmt.Errorf("%w: reference %q must be a canonical local Bead ID or an absolute URI", ErrValidation, reference)
	}
	if claimsScope(scopeURL, reference) {
		path, kind, ok := SplitCanonicalURL(scopeURL, reference)
		if !ok || kind != KindBead {
			return Ref{}, fmt.Errorf("%w: reference %q claims the Scope but is not a Bead's canonical URL", ErrValidation, reference)
		}
		return Ref{inScope: true, path: path, pin: pin}, nil
	}
	return Ref{uri: reference, pin: pin}, nil
}

// IsZero reports the absent reference.
func (r Ref) IsZero() bool { return !r.inScope && r.uri == "" }

// InScope reports whether r names a Bead of this Scope.
func (r Ref) InScope() bool { return r.inScope }

// Path is the canonical Bead path of an in-Scope reference; "" otherwise.
func (r Ref) Path() string { return r.path }

// URI is the absolute URI of an external reference; "" otherwise.
func (r Ref) URI() string { return r.uri }

// Pin is the revision the reference was made against; "" when unpinned.
func (r Ref) Pin() string { return r.pin }

// Pinned reports whether a pin is present.
func (r Ref) Pinned() bool { return r.pin != "" }

// URL renders the reference's identity as an absolute URI: the external URI
// as written, or the in-Scope path resolved against scopeURL — which is why a
// Scope URL rotation rewrites no stored reference.
func (r Ref) URL(scopeURL string) string {
	if r.inScope {
		return CanonicalURL(scopeURL, r.path)
	}
	return r.uri
}

// SameURI is reference identity: the two point at the same thing, pins
// ignored. It is the comparison endpoint equality, incident traversal and
// multiplicity use.
func (r Ref) SameURI(o Ref) bool {
	return r.inScope == o.inScope && r.path == o.path && r.uri == o.uri
}

// Equal is whole-value equality, pin included.
func (r Ref) Equal(o Ref) bool { return r == o }

// Bead is one identified, typed node of the graph: an immutable canonical path
// and Type, the revision naming its current state, its authored properties,
// and its carried attribution when one was recorded. Its owned Links are
// semantically covered Bead state but are not duplicated inside the value:
// BeadRecord carries them, assembled from the Links themselves in the same
// snapshot.
//
// DECISION: the value carries no provenance (last_authority_id, last_epoch)
// and no timestamps. Those are storage bookkeeping the design keeps on the
// row, not protocol data, and a public value that carried an authority field
// would be one step from a request that did.
type Bead struct {
	path        string
	typeURL     string
	revision    Revision
	attribution Attribution
	properties  Properties
}

// BeadSpec is the input to NewBead.
type BeadSpec struct {
	// Path is the canonical Scope-relative Bead path ("beads/…").
	Path string
	// TypeURL is the declared Type's canonical descriptor URL.
	TypeURL string
	// Revision names the current state; required.
	Revision Revision
	// Attribution is the carried attribution; the zero value means absent.
	Attribution Attribution
	// Properties is the authored object; the zero value is the empty object.
	Properties Properties
}

// NewBead builds a Bead, enforcing the path grammar, the Type URL law and a
// present revision.
func NewBead(spec BeadSpec) (Bead, error) {
	if err := ValidateBeadPath(spec.Path); err != nil {
		return Bead{}, err
	}
	if err := ValidateTypeURL(spec.TypeURL); err != nil {
		return Bead{}, fmt.Errorf("bead %s type: %w", spec.Path, err)
	}
	if spec.Revision.IsZero() {
		return Bead{}, fmt.Errorf("%w: bead %s has no revision", ErrValidation, spec.Path)
	}
	return Bead{
		path:        spec.Path,
		typeURL:     spec.TypeURL,
		revision:    spec.Revision,
		attribution: spec.Attribution,
		properties:  spec.Properties,
	}, nil
}

// Path is the canonical Scope-relative path.
func (b Bead) Path() string { return b.path }

// TypeURL is the declared Type's descriptor URL.
func (b Bead) TypeURL() string { return b.typeURL }

// Revision names the current state.
func (b Bead) Revision() Revision { return b.revision }

// Attribution returns the carried attribution and whether one is present.
func (b Bead) Attribution() (Attribution, bool) { return b.attribution, !b.attribution.IsZero() }

// Properties is the authored object.
func (b Bead) Properties() Properties { return b.properties }

// URL is the absolute canonical Bead URL under scopeURL.
func (b Bead) URL(scopeURL string) string { return CanonicalURL(scopeURL, b.path) }

// IsZero reports a Bead no constructor produced.
func (b Bead) IsZero() bool { return b.path == "" }

// Link is one first-class directed relationship: its own canonical path, Type
// and revision, an immutable source and target, authored properties, and a
// carried attribution when one was recorded. Its type, source and target
// describe it but do not identify it; several Links may share all three.
//
// The endpoints are SYMMETRIC: each is a Ref — an in-Scope Bead or an
// external URI, pinned or not — under the pinned spec's one endpoint law,
// "at least one endpoint of every BDP v0 Link MUST be an in-Scope Bead". An
// external source with an in-Scope target is a valid Link; the pinned Read
// fixtures carry three (external:beads:mol-run-assignee → beads/demo-f,
// urn:external:pin-witness → beads/demo-f pinned, urn:external:
// collation-witness → beads/demo-f), and a domain that could not admit them
// could not serve the pinned matrix.
//
// DECISION (P0 council, 2026-09-07): symmetry. An earlier draft of B2/B4
// fixed the source as a stored path (LinkSelectRequest.SourcePath; a NOT
// NULL source_path with a foreign key), which narrowed the spec and refused
// the fixtures above. B4 now mirrors the target's columns on the source
// (source_kind / source_path / source_url / source_pin, the path nullable,
// the foreign key skipped for an external source, a CHECK that at least one
// endpoint is in-Scope); the P1 migration must follow that revised B4 and
// never the earlier NOT NULL column.
type Link struct {
	path        string
	typeURL     string
	revision    Revision
	source      Ref
	target      Ref
	attribution Attribution
	properties  Properties
}

// LinkSpec is the input to NewLink.
type LinkSpec struct {
	// Path is the canonical Scope-relative Link path ("links/…").
	Path string
	// TypeURL is the declared Link Type's canonical descriptor URL.
	TypeURL string
	// Revision names the current state; required.
	Revision Revision
	// Source is where the Link leaves from: an in-Scope Bead or an external
	// URI, exactly like Target.
	Source Ref
	// Target is where the Link points: an in-Scope Bead or an external URI.
	Target Ref
	// Attribution is the carried attribution; the zero value means absent.
	Attribution Attribution
	// Properties is the authored object; the zero value is the empty object.
	Properties Properties
}

// NewLink builds a Link, enforcing the path grammar, the Type URL law, a
// present revision, two present endpoints, and the endpoint law: at least one
// endpoint is a Bead of this Scope. A Link between two external URIs is
// refused — a Scope cannot own one in v0.
func NewLink(spec LinkSpec) (Link, error) {
	if err := ValidateLinkPath(spec.Path); err != nil {
		return Link{}, err
	}
	if err := ValidateTypeURL(spec.TypeURL); err != nil {
		return Link{}, fmt.Errorf("link %s type: %w", spec.Path, err)
	}
	if spec.Revision.IsZero() {
		return Link{}, fmt.Errorf("%w: link %s has no revision", ErrValidation, spec.Path)
	}
	if spec.Source.IsZero() {
		return Link{}, fmt.Errorf("%w: link %s has no source", ErrValidation, spec.Path)
	}
	if spec.Target.IsZero() {
		return Link{}, fmt.Errorf("%w: link %s has no target", ErrValidation, spec.Path)
	}
	if !spec.Source.InScope() && !spec.Target.InScope() {
		return Link{}, fmt.Errorf("%w: link %s must have at least one in-Scope endpoint", ErrValidation, spec.Path)
	}
	return Link{
		path:        spec.Path,
		typeURL:     spec.TypeURL,
		revision:    spec.Revision,
		source:      spec.Source,
		target:      spec.Target,
		attribution: spec.Attribution,
		properties:  spec.Properties,
	}, nil
}

// Path is the canonical Scope-relative path.
func (l Link) Path() string { return l.path }

// TypeURL is the declared Type's descriptor URL.
func (l Link) TypeURL() string { return l.typeURL }

// Revision names the current state.
func (l Link) Revision() Revision { return l.revision }

// Source is the source reference: an in-Scope Bead or an external URI.
func (l Link) Source() Ref { return l.source }

// Target is the target reference: an in-Scope Bead or an external URI.
func (l Link) Target() Ref { return l.target }

// Attribution returns the carried attribution and whether one is present.
func (l Link) Attribution() (Attribution, bool) { return l.attribution, !l.attribution.IsZero() }

// Properties is the authored object.
func (l Link) Properties() Properties { return l.properties }

// URL is the absolute canonical Link URL under scopeURL.
func (l Link) URL(scopeURL string) string { return CanonicalURL(scopeURL, l.path) }

// IsZero reports a Link no constructor produced.
func (l Link) IsZero() bool { return l.path == "" }

// ExternalPolicy is a Link Type endpoint's external-endpoint policy: what an
// out-of-Scope reference at that endpoint may be when the Link is created.
type ExternalPolicy string

// The three policies. An absent member means ExternalOpaque.
const (
	// ExternalNone rejects an out-of-Scope reference at the endpoint.
	ExternalNone ExternalPolicy = "none"
	// ExternalOpaque admits any external URI.
	ExternalOpaque ExternalPolicy = "opaque"
	// ExternalBead admits an external URI only when it is bead-shaped: a
	// canonical HTTP(S) URL whose path contains a beads/{id} tail. Declared
	// intent about creation time, never an ongoing guarantee.
	ExternalBead ExternalPolicy = "bead"
)

// Valid reports whether p is one of the three policies.
func (p ExternalPolicy) Valid() bool {
	return p == ExternalNone || p == ExternalOpaque || p == ExternalBead
}

// EndpointConstraint is one endpoint's constraint on a Link Type Descriptor:
// the Types an in-Scope Bead at that endpoint must conform to (every one
// required; empty accepts any Bead) and the external-endpoint policy.
type EndpointConstraint struct {
	conformsTo []string
	external   ExternalPolicy // "" when the member is absent
}

// NewEndpointConstraint builds a constraint. conformsTo holds canonical Type
// URLs with no duplicates — a set, kept in code-unit order — and external is
// a policy or "" for absent. The zero EndpointConstraint is the constraint
// that requires nothing (any in-Scope Bead; external opaque) and is admitted
// by NewTypeDescriptor as exactly that.
func NewEndpointConstraint(conformsTo []string, external ExternalPolicy) (EndpointConstraint, error) {
	urls, err := typeURLList(conformsTo, "endpoint conformsTo")
	if err != nil {
		return EndpointConstraint{}, err
	}
	if external != "" && !external.Valid() {
		return EndpointConstraint{}, fmt.Errorf("%w: endpoint external policy %q is not none, opaque or bead", ErrValidation, external)
	}
	return EndpointConstraint{conformsTo: urls, external: external}, nil
}

// ConformsTo returns a copy of the required Type URLs, in code-unit order.
func (c EndpointConstraint) ConformsTo() []string { return append([]string(nil), c.conformsTo...) }

// normalized returns the constraint with a nil (zero-value) conformsTo made
// the empty set, so the canonical form always carries an array.
func (c EndpointConstraint) normalized() EndpointConstraint {
	if c.conformsTo == nil {
		c.conformsTo = []string{}
	}
	return c
}

// External returns the declared policy and whether the member is present.
func (c EndpointConstraint) External() (ExternalPolicy, bool) { return c.external, c.external != "" }

// EffectiveExternal is the policy in force: the declared one, or
// ExternalOpaque when absent.
func (c EndpointConstraint) EffectiveExternal() ExternalPolicy {
	if c.external == "" {
		return ExternalOpaque
	}
	return c.external
}

// WildcardOwnedLinkKey is the reserved ownsOutgoing key "*" (bdp#1 item 5,
// ruled 2026-09-08): a Bead Type MAY carry the entry "*": { max }, declaring
// every outgoing Link Type it does not list explicitly as owned. It is a KEY,
// never a Type: ValidateTypeURL refuses it, so no Link's type, no descriptor
// or endpoint conformsTo entry, no descriptor id and no ownedLinks group key
// can spell it.
//
// The Read foundation vendored by bdpwire at 19923f5b carries this declaration
// as the distinct max-only ownedWildcardDeclaration. Record ownedLinks keys
// remain actual Link Type URLs. bdpwire checks both carriers against the
// pinned key pattern; this domain layer also holds canonical Type identity.
const WildcardOwnedLinkKey = "*"

// OwnedLinkDecl is one ownsOutgoing entry of a Bead Type Descriptor. It is a
// SUM: an EXPLICIT declaration names one owned Link Type and may carry a
// display label (descriptor-only, never in a Resource record); the WILDCARD
// declaration, keyed WildcardOwnedLinkKey, owns every outgoing Link Type the
// descriptor does not name explicitly and carries no label. Both carry the
// required bound.
//
// DECISION: the wildcard is the reserved key INSIDE the declaration list, not
// a separate descriptor member. The canonical form then falls out of the
// existing map ("*" sorts first — 0x2A precedes every URL's "h" — so the
// fingerprint is the wire form's), the duplicate law
// covers "two wildcards" unchanged, and — the reason that matters most — a
// consumer walking OwnsOutgoing() to assemble ownedLinks cannot overlook it:
// it meets a declaration whose Wildcard() is true, and a record that keyed a
// group by it fails CheckBeadRecord. A separate accessor would let that
// consumer silently serve an incomplete record.
type OwnedLinkDecl struct {
	typeURL string // the owned Link Type, or WildcardOwnedLinkKey
	label   string // "" when absent; always "" on the wildcard
	max     int
}

// NewOwnedLinkDecl builds an explicit declaration. Max is REQUIRED and
// positive — the installer refuses an owning declaration without a bound,
// because the owned plane must always be servable inline. label "" means
// absent. The wildcard key is refused here (ValidateTypeURL): the wildcard
// is built by NewWildcardOwnedLinkDecl, never by naming "*" as a Type.
func NewOwnedLinkDecl(linkTypeURL, label string, max int) (OwnedLinkDecl, error) {
	if err := ValidateTypeURL(linkTypeURL); err != nil {
		return OwnedLinkDecl{}, fmt.Errorf("ownsOutgoing key: %w", err)
	}
	if err := validateOwnedMax(linkTypeURL, max); err != nil {
		return OwnedLinkDecl{}, err
	}
	if err := validUTF8(label, "ownsOutgoing "+linkTypeURL+": label"); err != nil {
		return OwnedLinkDecl{}, err
	}
	return OwnedLinkDecl{typeURL: linkTypeURL, label: label, max: max}, nil
}

// NewWildcardOwnedLinkDecl builds the wildcard declaration: every outgoing
// Link Type the descriptor does not name explicitly is owned, and max bounds
// the Bead's WHOLE OWNED SET — every owned Link of one Bead across every
// owned Link Type together, the explicitly declared Types' Links included —
// not each Type separately, since the Types are open-ended and a per-Type
// bound would bound nothing inline. Max is required and positive exactly as
// on an explicit declaration; the installer's "no owning declaration without
// a bound" law covers both.
//
// DECISION: no label. The ruling spells the entry as "*": { max }, and a
// label is display documentation for one named Type; the wildcard names
// none, so a label on it is refused — conservative, because admitting one
// later is a widening while refusing one later would break installed
// descriptors.
//
// DECISION (OW1 = A, operator ruling 2026-09-08, gastownhall/bdp#22): "max
// bounds the whole owned set" is the LITERAL whole set. P0's first reading
// took the narrower set — the Links owned by virtue of the wildcard alone,
// each explicit Type bounded only by its own max — and was flipped: the
// wildcard's max is the ONE number that bounds a Bead's inline owned plane,
// and an explicit declaration's max is a tighter per-Type bound inside it
// ("explicit entries take precedence for the types they name" says which
// declaration owns a Type, not that the Type escapes the bound). It follows
// that an explicit max above the wildcard's promises what the wildcard
// forbids, so such a descriptor is invalid and is not installed:
// NewTypeDescriptor, and ParseTypeDescriptor through it, refuse it naming
// the Type and both numbers — a descriptor-validation rule beyond the
// schema bundle, which cannot relate two entries' numbers. Equal is
// admitted: the explicit Type may then fill the whole set by itself.
func NewWildcardOwnedLinkDecl(max int) (OwnedLinkDecl, error) {
	if err := validateOwnedMax(WildcardOwnedLinkKey, max); err != nil {
		return OwnedLinkDecl{}, err
	}
	return OwnedLinkDecl{typeURL: WildcardOwnedLinkKey, max: max}, nil
}

// validateOwnedMax is the max law both declarations share: positive, and —
// because a descriptor's canonical form spells max as a JSON number — under
// the binary64 admission law (bdp#21), so that every descriptor that exists
// has a canonical form ParseTypeDescriptor re-admits.
//
// DECISION: the law is applied at this door with the SAME predicate the
// canonicalizer uses, not with a simpler 2^53 cap. 9007199254740994 is then
// admitted at both doors and 9007199254740993 refused at both, so the JSON
// door and the Go constructor agree on exactly which descriptors exist; a
// cap would have made the constructor stricter than the wire for no
// consumer's benefit.
func validateOwnedMax(key string, max int) error {
	if max < 1 {
		return fmt.Errorf("%w: ownsOutgoing %s: max must be a positive integer", ErrValidation, key)
	}
	if !admissibleInt(max) {
		return fmt.Errorf("%w: ownsOutgoing %s: max %d does not round-trip through IEEE-754 binary64 (bdp#21)", ErrValidation, key, max)
	}
	return nil
}

// Wildcard reports whether d is the wildcard declaration.
func (d OwnedLinkDecl) Wildcard() bool { return d.typeURL == WildcardOwnedLinkKey }

// TypeURL is the owned Link Type — or WildcardOwnedLinkKey for the wildcard
// declaration, which names no Type: check Wildcard() before using the value
// as a URL.
func (d OwnedLinkDecl) TypeURL() string { return d.typeURL }

// Label returns the display label and whether one is present; never present
// on the wildcard.
func (d OwnedLinkDecl) Label() (string, bool) { return d.label, d.label != "" }

// Max is the largest owned set the declaration permits: the Links of the one
// named Type for an explicit declaration; for the wildcard, the Bead's whole
// owned set — every owned Link across every owned Type, the explicitly
// declared Types' Links included (OW1 = A). In a descriptor that exists no
// explicit Max exceeds the wildcard's, so the wildcard's Max alone sizes the
// inline owned plane of a Bead whose Type carries one.
func (d OwnedLinkDecl) Max() int { return d.max }

// TypeDescriptor is one installed Type contract: the closed BDP v0 descriptor
// shape, held with its canonical JSON and the fingerprint the installer keys
// idempotence on. Its ID is immutable and names one immutable contract;
// changing the category, the conformance graph, the properties constraints or
// the endpoint constraints is a new Type ID.
type TypeDescriptor struct {
	id               string
	name             string
	description      string // "" when absent
	describes        ResourceKind
	conformsTo       []string
	propertiesSchema string // "" when absent
	source, target   EndpointConstraint
	ownsOutgoing     []OwnedLinkDecl // sorted by key in code-unit order: the wildcard ("*") first when present, then Type URLs
	canonical        []byte
	fingerprint      string
}

// TypeDescriptorSpec is the input to NewTypeDescriptor. Optional string
// members are absent when empty.
type TypeDescriptorSpec struct {
	// ID is the absolute canonical Type ID.
	ID string
	// Name is the required human-readable name; it does not establish identity.
	Name string
	// Description is optional documentation; "" means absent.
	Description string
	// Describes is the Resource category.
	Describes ResourceKind
	// ConformsTo lists direct parent Type IDs, unique, in no significant
	// order: it is a set (the bundle's uniqueItems), and the canonical form
	// sorts it by code unit, so reordered parents fingerprint identically.
	ConformsTo []string
	// PropertiesSchema is the absolute URL of the properties JSON Schema; ""
	// means absent.
	PropertiesSchema string
	// Source and Target are required for a Link Type and must be nil for a
	// Bead Type. A pointer to the zero EndpointConstraint is the constraint
	// that requires nothing.
	Source, Target *EndpointConstraint
	// OwnsOutgoing declares the owned Link Types of a Bead Type: explicit
	// declarations and at most one wildcard (NewWildcardOwnedLinkDecl), in
	// any order, no explicit max above the wildcard's (OW1 = A); empty means
	// the member is absent. Must be empty for a Link Type.
	OwnsOutgoing []OwnedLinkDecl
}

// NewTypeDescriptor builds a descriptor under the closed-shape laws of the
// pinned spec and schema bundle: a canonical Type ID, a nonempty name, a
// category, unique canonical parent IDs that do not include the Type itself,
// endpoint constraints exactly when the Type describes Links, ownsOutgoing
// only when it describes Beads, one declaration per owned Link Type, at
// most one wildcard (bdp#1 item 5, carried by the Read foundation — see
// WildcardOwnedLinkKey), and — under a wildcard — no explicit declaration
// whose max exceeds the wildcard's, since the wildcard's max bounds the
// whole owned set (OW1 = A; see NewWildcardOwnedLinkDecl).
func NewTypeDescriptor(spec TypeDescriptorSpec) (TypeDescriptor, error) {
	if err := ValidateTypeURL(spec.ID); err != nil {
		return TypeDescriptor{}, fmt.Errorf("descriptor id: %w", err)
	}
	if spec.Name == "" {
		return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: name must be nonempty", ErrValidation, spec.ID)
	}
	if err := validUTF8(spec.Name, "descriptor "+spec.ID+": name"); err != nil {
		return TypeDescriptor{}, err
	}
	if err := validUTF8(spec.Description, "descriptor "+spec.ID+": description"); err != nil {
		return TypeDescriptor{}, err
	}
	if !spec.Describes.Valid() {
		return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: describes must be bead or link", ErrValidation, spec.ID)
	}
	conformsTo, err := typeURLList(spec.ConformsTo, "descriptor "+spec.ID+" conformsTo")
	if err != nil {
		return TypeDescriptor{}, err
	}
	for _, parent := range conformsTo {
		if parent == spec.ID {
			return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s conforms to itself", ErrValidation, spec.ID)
		}
	}
	if spec.PropertiesSchema != "" {
		if err := ValidateTypeURL(spec.PropertiesSchema); err != nil {
			return TypeDescriptor{}, fmt.Errorf("descriptor %s propertiesSchema: %w", spec.ID, err)
		}
	}
	d := TypeDescriptor{
		id:               spec.ID,
		name:             spec.Name,
		description:      spec.Description,
		describes:        spec.Describes,
		conformsTo:       conformsTo,
		propertiesSchema: spec.PropertiesSchema,
	}
	switch spec.Describes {
	case KindLink:
		if spec.Source == nil || spec.Target == nil {
			return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: a Link Type declares both source and target constraints", ErrValidation, spec.ID)
		}
		if len(spec.OwnsOutgoing) != 0 {
			return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: a Link Type cannot own outgoing Links", ErrValidation, spec.ID)
		}
		d.source, d.target = spec.Source.normalized(), spec.Target.normalized()
	case KindBead:
		if spec.Source != nil || spec.Target != nil {
			return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: a Bead Type has no endpoint constraints", ErrValidation, spec.ID)
		}
		owns := append([]OwnedLinkDecl(nil), spec.OwnsOutgoing...)
		sort.SliceStable(owns, func(i, j int) bool {
			return CompareCodeUnits(owns[i].typeURL, owns[j].typeURL) < 0
		})
		for i, decl := range owns {
			if decl.typeURL == "" {
				return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: ownsOutgoing entry %d is not a constructed declaration", ErrValidation, spec.ID, i)
			}
			if i > 0 && owns[i-1].typeURL == decl.typeURL {
				// The same law refuses two wildcards: both are keyed "*".
				return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: ownsOutgoing declares %s twice", ErrValidation, spec.ID, decl.typeURL)
			}
		}
		// The whole-set rule (OW1 = A): the wildcard's max bounds every owned
		// Link of a Bead, explicit Types' Links included, so an explicit max
		// above it is refused. The wildcard sorts first ("*" precedes every
		// URL), so it is owns[0] when present.
		if len(owns) > 0 && owns[0].Wildcard() {
			for _, decl := range owns[1:] {
				if decl.max > owns[0].max {
					return TypeDescriptor{}, fmt.Errorf("%w: descriptor %s: ownsOutgoing %s: max %d exceeds the wildcard's max %d, which bounds the whole owned set (OW1 = A)", ErrValidation, spec.ID, decl.typeURL, decl.max, owns[0].max)
				}
			}
		}
		d.ownsOutgoing = owns
	}
	d.canonical = descriptorCanonicalJSON(d)
	d.fingerprint = hashHex(d.canonical)
	return d, nil
}

// descriptorWire is the closed JSON shape of a descriptor, members in the
// code-unit order of their names so encoding/json already emits them sorted;
// CanonicalizeJSON afterwards guarantees the RFC 8785 form regardless.
type descriptorWire struct {
	ConformsTo       []string                 `json:"conformsTo"`
	Describes        ResourceKind             `json:"describes"`
	Description      *string                  `json:"description,omitempty"`
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	OwnsOutgoing     map[string]ownedLinkWire `json:"ownsOutgoing,omitempty"`
	PropertiesSchema *string                  `json:"propertiesSchema,omitempty"`
	Source           *endpointConstraintWire  `json:"source,omitempty"`
	Target           *endpointConstraintWire  `json:"target,omitempty"`
}

type ownedLinkWire struct {
	Label *string `json:"label,omitempty"`
	Max   int     `json:"max"`
}

type endpointConstraintWire struct {
	ConformsTo []string        `json:"conformsTo"`
	External   *ExternalPolicy `json:"external,omitempty"`
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// endpointWire renders a constraint; the descriptor holds normalized
// constraints (never a nil conformsTo), so an empty set encodes as [] and
// never as null.
func endpointWire(c EndpointConstraint) *endpointConstraintWire {
	w := &endpointConstraintWire{ConformsTo: c.conformsTo}
	if c.external != "" {
		ext := c.external
		w.External = &ext
	}
	return w
}

// descriptorCanonicalJSON renders a validated descriptor's canonical bytes.
// Every member is a validated string, an int or a non-nil slice, so neither
// the encoder nor the canonicalizer can fail on it.
func descriptorCanonicalJSON(d TypeDescriptor) []byte {
	w := descriptorWire{
		ConformsTo:       d.conformsTo,
		Describes:        d.describes,
		Description:      optionalString(d.description),
		ID:               d.id,
		Name:             d.name,
		PropertiesSchema: optionalString(d.propertiesSchema),
	}
	if d.describes == KindLink {
		w.Source, w.Target = endpointWire(d.source), endpointWire(d.target)
	}
	if len(d.ownsOutgoing) > 0 {
		w.OwnsOutgoing = make(map[string]ownedLinkWire, len(d.ownsOutgoing))
		// The wildcard's key is "*" (WildcardOwnedLinkKey), the wire spelling
		// the ruling gives it; CanonicalizeJSON sorts it before every URL.
		for _, decl := range d.ownsOutgoing {
			w.OwnsOutgoing[decl.typeURL] = ownedLinkWire{Label: optionalString(decl.label), Max: decl.max}
		}
	}
	raw, _ := json.Marshal(w)
	canonical, _ := CanonicalizeJSON(raw)
	return canonical
}

// memberShape is the JSON type a descriptor member must have: the first byte
// its canonical value begins with, and the type's name for diagnostics.
type memberShape struct {
	first byte
	name  string
}

var (
	jsonString = memberShape{'"', "string"}
	jsonArray  = memberShape{'[', "array"}
	jsonObject = memberShape{'{', "object"}
)

// descriptorMembers is the closed shape of a descriptor document: every
// member the bundle's typeDescriptor definition declares, with the JSON type
// it must have. Nothing here is nullable; presence and the conditional
// members are decided by ParseTypeDescriptor and NewTypeDescriptor.
var descriptorMembers = map[string]memberShape{
	"id": jsonString, "name": jsonString, "description": jsonString, "describes": jsonString,
	"conformsTo": jsonArray, "propertiesSchema": jsonString, "source": jsonObject, "target": jsonObject,
	"ownsOutgoing": jsonObject,
}

// ParseTypeDescriptor admits a descriptor from its JSON representation — a
// catalog file, the wire — under the closed shape: member names are exact
// and case-sensitive (a stranger, "ID" and "conformsto" included, is
// refused), a duplicate key is refused, every present member has the type
// the bundle gives it and is never null (absent and null are different
// things, and the bundle admits only absence), the required members are
// present, a Bead Type carries no endpoint constraint and a Link Type no
// ownsOutgoing, propertiesSchema when present is an absolute URL, an
// ownsOutgoing key is a canonical Link Type URL or the wildcard "*"
// (WildcardOwnedLinkKey, now carried by the pinned Read foundation), and
// every law NewTypeDescriptor enforces holds — the whole-set max rule among
// them: an explicit entry's max above the wildcard's is refused (OW1 = A),
// a rule the schema bundle cannot state
// because it relates two entries' numbers.
//
// DECISION: description "" is read as absent. The bundle permits the empty
// string, the canonical form has no way to carry it apart from absence, and
// nothing in the protocol distinguishes the two; propertiesSchema "" is
// refused instead, because an empty string is not the absolute URL the
// member must be.
func ParseTypeDescriptor(raw []byte) (TypeDescriptor, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return TypeDescriptor{}, fmt.Errorf("%w: descriptor: %s", ErrValidation, reason(err))
	}
	fail := func(format string, args ...any) (TypeDescriptor, error) {
		return TypeDescriptor{}, fmt.Errorf("%w: descriptor: %s", ErrValidation, fmt.Sprintf(format, args...))
	}
	if canonical[0] != '{' {
		return fail("must be a JSON object")
	}
	members := map[string][]byte{}
	for _, m := range canonicalObjectMembers(canonical) {
		want, known := descriptorMembers[m.key]
		if !known {
			return fail("unknown member %q (member names are exact and case-sensitive)", m.key)
		}
		if m.value[0] == 'n' {
			return fail("member %q must not be null (omit it instead)", m.key)
		}
		if m.value[0] != want.first {
			return fail("member %q must be a JSON %s", m.key, want.name)
		}
		members[m.key] = m.value
	}
	for _, required := range []string{"id", "name", "describes", "conformsTo"} {
		if _, ok := members[required]; !ok {
			return fail("member %q is required", required)
		}
	}
	spec := TypeDescriptorSpec{
		ID:        canonicalStringValue(members["id"]),
		Name:      canonicalStringValue(members["name"]),
		Describes: ResourceKind(canonicalStringValue(members["describes"])),
	}
	if v, ok := members["description"]; ok {
		spec.Description = canonicalStringValue(v)
	}
	if v, ok := members["propertiesSchema"]; ok {
		spec.PropertiesSchema = canonicalStringValue(v)
		if spec.PropertiesSchema == "" {
			return fail("propertiesSchema must be an absolute URL when present, not \"\"")
		}
	}
	if spec.ConformsTo, err = parseTypeIDArray(members["conformsTo"], "conformsTo"); err != nil {
		return TypeDescriptor{}, err
	}
	for _, end := range []string{"source", "target"} {
		v, ok := members[end]
		if !ok {
			continue
		}
		if spec.Describes == KindBead {
			return fail("a Bead Type has no %s member", end)
		}
		c, err := parseEndpointConstraint(v, end)
		if err != nil {
			return TypeDescriptor{}, err
		}
		if end == "source" {
			spec.Source = &c
		} else {
			spec.Target = &c
		}
	}
	if v, ok := members["ownsOutgoing"]; ok {
		entries := canonicalObjectMembers(v)
		if len(entries) == 0 {
			return fail("ownsOutgoing must declare at least one Link Type when present")
		}
		for _, entry := range entries {
			decl, err := parseOwnedLinkDecl(entry.key, entry.value)
			if err != nil {
				return TypeDescriptor{}, err
			}
			spec.OwnsOutgoing = append(spec.OwnsOutgoing, decl)
		}
	}
	return NewTypeDescriptor(spec)
}

// parseTypeIDArray reads a canonical array whose elements must all be
// strings; the URLs themselves are validated by typeURLList later.
func parseTypeIDArray(canonical []byte, what string) ([]string, error) {
	var out []string
	for _, v := range canonicalArrayValues(canonical) {
		if v[0] != '"' {
			return nil, fmt.Errorf("%w: descriptor: %s must be an array of Type URL strings", ErrValidation, what)
		}
		out = append(out, canonicalStringValue(v))
	}
	return out, nil
}

func parseEndpointConstraint(canonical []byte, member string) (EndpointConstraint, error) {
	fail := func(format string, args ...any) (EndpointConstraint, error) {
		return EndpointConstraint{}, fmt.Errorf("%w: descriptor %s: %s", ErrValidation, member, fmt.Sprintf(format, args...))
	}
	var conformsTo []string
	hasConformsTo, external := false, ExternalPolicy("")
	for _, m := range canonicalObjectMembers(canonical) {
		switch m.key {
		case "conformsTo":
			if m.value[0] != '[' {
				return fail("conformsTo must be an array of Type URLs, never null")
			}
			list, err := parseTypeIDArray(m.value, member+".conformsTo")
			if err != nil {
				return EndpointConstraint{}, err
			}
			conformsTo, hasConformsTo = list, true
		case "external":
			if m.value[0] != '"' {
				return fail("external must be a string policy, never null")
			}
			external = ExternalPolicy(canonicalStringValue(m.value))
			if external == "" {
				return fail("external policy must be none, opaque or bead")
			}
		default:
			return fail("unknown member %q (member names are exact and case-sensitive)", m.key)
		}
	}
	if !hasConformsTo {
		return fail("conformsTo is required")
	}
	return NewEndpointConstraint(conformsTo, external)
}

// parseOwnedLinkDecl reads one ownsOutgoing entry. The key is the wildcard
// (WildcardOwnedLinkKey) or a Link Type URL; the value's shape is the same
// for both, except that the wildcard refuses a label.
func parseOwnedLinkDecl(key string, canonical []byte) (OwnedLinkDecl, error) {
	fail := func(format string, args ...any) (OwnedLinkDecl, error) {
		return OwnedLinkDecl{}, fmt.Errorf("%w: descriptor: ownsOutgoing %s: %s", ErrValidation, key, fmt.Sprintf(format, args...))
	}
	if canonical[0] != '{' {
		return fail("declaration must be an object, never null")
	}
	var max int
	hasMax, label := false, ""
	for _, m := range canonicalObjectMembers(canonical) {
		switch m.key {
		case "max":
			// The canonical form spells every integral number as a plain
			// integer (1.0 and 1e0 are "1"), so a canonical value that
			// ParseInt refuses is fractional, out of range, or not a number.
			// A value the binary64 admission law refuses never reaches here:
			// CanonicalizeJSON refused the document, naming this member.
			n, err := strconv.ParseInt(string(m.value), 10, 0)
			if err != nil {
				return fail("max must be an integer within the platform int range, got %s", m.value)
			}
			max, hasMax = int(n), true
		case "label":
			if m.value[0] != '"' {
				return fail("label must be a string, never null")
			}
			label = canonicalStringValue(m.value)
			if label == "" {
				return fail("label must be nonempty when present")
			}
		default:
			return fail("unknown member %q (member names are exact and case-sensitive)", m.key)
		}
	}
	if !hasMax {
		return fail("max is required")
	}
	if key == WildcardOwnedLinkKey {
		if label != "" {
			return fail("the wildcard declaration carries no label")
		}
		return NewWildcardOwnedLinkDecl(max)
	}
	return NewOwnedLinkDecl(key, label, max)
}

// typeURLList validates a Type-ID array — canonical URLs, no duplicates —
// and returns it in code-unit order. The lists are sets (the bundle's
// uniqueItems), so the canonical form, and with it the fingerprint, is
// independent of the authored order (P0 council, 2026-09-07).
func typeURLList(urls []string, what string) ([]string, error) {
	out := make([]string, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		if err := ValidateTypeURL(u); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if _, dup := seen[u]; dup {
			return nil, fmt.Errorf("%w: %s lists %s twice", ErrValidation, what, u)
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool { return CompareCodeUnits(out[i], out[j]) < 0 })
	return out, nil
}

// ID is the absolute canonical Type ID.
func (t TypeDescriptor) ID() string { return t.id }

// Name is the human-readable name.
func (t TypeDescriptor) Name() string { return t.name }

// Description returns the documentation and whether one is present.
func (t TypeDescriptor) Description() (string, bool) { return t.description, t.description != "" }

// Describes is the Resource category.
func (t TypeDescriptor) Describes() ResourceKind { return t.describes }

// ConformsTo returns a copy of the direct parent Type IDs, in code-unit order.
func (t TypeDescriptor) ConformsTo() []string { return append([]string(nil), t.conformsTo...) }

// PropertiesSchema returns the schema URL and whether one is present.
func (t TypeDescriptor) PropertiesSchema() (string, bool) {
	return t.propertiesSchema, t.propertiesSchema != ""
}

// Source returns the source constraint and whether the Type describes Links.
func (t TypeDescriptor) Source() (EndpointConstraint, bool) { return t.source, t.describes == KindLink }

// Target returns the target constraint and whether the Type describes Links.
func (t TypeDescriptor) Target() (EndpointConstraint, bool) { return t.target, t.describes == KindLink }

// OwnsOutgoing returns a copy of the owned-Link declarations in code-unit
// order of key: the wildcard first when present ("*" sorts before every
// URL), then the explicit declarations by Link Type URL — the order their
// ownedLinks groups are served in, with the wildcard's own groups (one per
// wildcard-owned Type actually present) interleaved among them by URL.
func (t TypeDescriptor) OwnsOutgoing() []OwnedLinkDecl {
	return append([]OwnedLinkDecl(nil), t.ownsOutgoing...)
}

// Owns returns the declaration under which this Type owns Links of Type
// linkTypeURL and whether it owns them: the explicit declaration naming the
// Type when there is one, else the wildcard declaration when there is one,
// else none — "explicit entries take precedence for the types they name".
// It is the owned-Link trigger law in predicate form: a mutation of a Link
// whose Type the source's Bead Type owns versions the source. Precedence
// says WHICH declaration owns a Type, not what bounds it: the returned
// explicit declaration's Max bounds that Type's Links, and when the
// descriptor also carries a wildcard, the wildcard's Max bounds the Bead's
// whole owned set, those Links included (OW1 = A) — a caller sizing the
// owned plane reads the wildcard's Max from OwnsOutgoing, not a sum.
//
// DECISION: Owns(WildcardOwnedLinkKey) is false. "*" is a key, not a Type;
// no Link carries it, so nothing is owned under it BY NAME, and answering
// the wildcard declaration there would hand a caller a "Type" it could key
// a group by.
func (t TypeDescriptor) Owns(linkTypeURL string) (OwnedLinkDecl, bool) {
	if linkTypeURL == WildcardOwnedLinkKey {
		return OwnedLinkDecl{}, false
	}
	var wildcard OwnedLinkDecl
	for _, decl := range t.ownsOutgoing {
		switch {
		case decl.typeURL == linkTypeURL:
			return decl, true
		case decl.Wildcard():
			wildcard = decl
		}
	}
	return wildcard, wildcard.Wildcard()
}

// CanonicalJSON returns a copy of the descriptor's canonical bytes: the closed
// shape, absent members omitted, in RFC 8785 form. It is what the storage leg
// stores and what the fingerprint covers.
func (t TypeDescriptor) CanonicalJSON() []byte { return append([]byte(nil), t.canonical...) }

// Fingerprint is the lowercase hex SHA-256 of CanonicalJSON: the installer's
// idempotence key and the descriptors table's UNIQUE column.
//
// DECISION: the fingerprint covers the descriptor alone, byte-level after
// canonicalization. The pinned spec's "internal integrity fingerprint" covers
// the whole contract closure (schemas included), which v0 does not fetch; a
// closure-inclusive fingerprint is a P1/P3 extension of this definition. The
// conformsTo sets (the descriptor's and each endpoint's) are sorted by code
// unit in the canonical form, so the authored order is NOT significant to
// it: two descriptors that differ only in parent order are one contract.
// Validation rules beyond the schema do not reach it either: the whole-set
// max rule (OW1 = A) decides which descriptors EXIST, not how one is spelled,
// so a descriptor it admits fingerprints exactly as it did before the ruling
// — the installer's idempotence key is stable across it — and a descriptor
// it refuses is never built and has no fingerprint here at all.
func (t TypeDescriptor) Fingerprint() string { return t.fingerprint }

// IsZero reports a descriptor no constructor produced.
func (t TypeDescriptor) IsZero() bool { return t.id == "" }

// WitnessClaim is what this workspace's witness claims about the Scope, as
// IdentityReader reports it. It is a REPORT, never an input: no request
// carries one.
type WitnessClaim struct {
	// Held reports whether this workspace holds a witness for the Scope.
	Held bool
	// Epoch is the authority epoch the witness names.
	Epoch uint64
	// LedgerSeq and LedgerHash are the ledger head the witness recorded.
	LedgerSeq  uint64
	LedgerHash string
	// Unverified is set by a restore (Admin.MarkUnverified) until a restore
	// verb clears it; every protected operation refuses while it is set.
	Unverified bool
	// Pending names the kind of an unfinished multi-phase transition, or ""
	// when none is recorded. Recovery runs before any assertion.
	Pending string
}

// ScopeIdentity is the Scope row plus this workspace's witness claim: what
// IdentityReader.Read answers and what every transition returns.
type ScopeIdentity struct {
	scopeURL    string
	authorityID string
	epoch       uint64
	mintedAt    time.Time
	claim       WitnessClaim
}

// ScopeIdentitySpec is the input to NewScopeIdentity.
type ScopeIdentitySpec struct {
	// ScopeURL is the canonical Scope URL the row holds.
	ScopeURL string
	// AuthorityID is the row's authority id (32 lowercase hex digits).
	AuthorityID string
	// Epoch is the row's authority epoch.
	Epoch uint64
	// MintedAt is when the Scope was minted.
	MintedAt time.Time
	// Claim is the witness's claim.
	Claim WitnessClaim
}

// NewScopeIdentity builds the report, enforcing the persisted Scope URL law
// (this is the Scope row's own identity), the id shape, a minted-at instant
// and, when a ledger hash is claimed, its shape.
func NewScopeIdentity(spec ScopeIdentitySpec) (ScopeIdentity, error) {
	if err := ValidatePersistedScopeURL(spec.ScopeURL); err != nil {
		return ScopeIdentity{}, err
	}
	if !isLowerHex(spec.AuthorityID, 32) {
		return ScopeIdentity{}, fmt.Errorf("%w: authority id must be 32 lowercase hex digits", ErrValidation)
	}
	if spec.MintedAt.IsZero() {
		return ScopeIdentity{}, fmt.Errorf("%w: Scope identity needs a minted-at instant", ErrValidation)
	}
	if spec.Claim.LedgerHash != "" && !isLowerHex(spec.Claim.LedgerHash, 64) {
		return ScopeIdentity{}, fmt.Errorf("%w: witness ledger hash must be 64 lowercase hex digits", ErrValidation)
	}
	return ScopeIdentity{
		scopeURL:    spec.ScopeURL,
		authorityID: spec.AuthorityID,
		epoch:       spec.Epoch,
		mintedAt:    spec.MintedAt.UTC(),
		claim:       spec.Claim,
	}, nil
}

// ScopeURL is the canonical Scope URL.
func (s ScopeIdentity) ScopeURL() string { return s.scopeURL }

// AuthorityID is the Scope row's authority id.
func (s ScopeIdentity) AuthorityID() string { return s.authorityID }

// Epoch is the Scope row's authority epoch.
func (s ScopeIdentity) Epoch() uint64 { return s.epoch }

// MintedAt is when the Scope was minted, in UTC.
func (s ScopeIdentity) MintedAt() time.Time { return s.mintedAt }

// Claim is the witness's claim.
func (s ScopeIdentity) Claim() WitnessClaim { return s.claim }

// IsZero reports an identity no constructor produced.
func (s ScopeIdentity) IsZero() bool { return s.scopeURL == "" }

// LedgerDurability is what a provider declares about its ledger across a
// database restore (ruling 11): whether the ledger is restored with the state
// it fences, survives independently of it, or does not survive at all.
type LedgerDurability string

// The three declarations.
const (
	// LedgerInState: the ledger lives in the versioned state and is restored
	// with it (Dolt, v0). A restore must show continuity through the ledger
	// lane or rotate.
	LedgerInState LedgerDurability = "in-state"
	// LedgerIndependent: the ledger survives a state restore on its own; a
	// restore re-validates ancestry and regrants.
	LedgerIndependent LedgerDurability = "independent"
	// LedgerNone: the ledger does not survive a restore; a restore always
	// rotates.
	LedgerNone LedgerDurability = "none"
)

// Valid reports whether d is one of the three declarations.
func (d LedgerDurability) Valid() bool {
	return d == LedgerInState || d == LedgerIndependent || d == LedgerNone
}

// LedgerEventKind is the closed set of ledger event kinds.
type LedgerEventKind string

// The eight kinds. Every mutation of the graph is one of them.
const (
	LedgerMint      LedgerEventKind = "mint"
	LedgerInstall   LedgerEventKind = "install"
	LedgerUpdate    LedgerEventKind = "update"
	LedgerPromote   LedgerEventKind = "promote"
	LedgerRotate    LedgerEventKind = "rotate"
	LedgerAllocate  LedgerEventKind = "allocate"
	LedgerTombstone LedgerEventKind = "tombstone"
	LedgerRefuseURL LedgerEventKind = "refuse_url"
)

// Valid reports whether k is one of the eight kinds.
func (k LedgerEventKind) Valid() bool {
	_, ok := ledgerShapes[k]
	return ok
}

// MaxLedgerSeq is the largest sequence number a ledger event may carry.
// Sequence numbers start at 1 (0 is LedgerRange's "unbounded" sentinel and
// never an event's number), and the counter records last_seq + 1 — the B4
// graph_ledger_seq row, LedgerApplyResult.NextSeq — so the number after the
// last event must itself be a uint64: a ledger whose head reached this value
// is EXHAUSTED and appends nothing more. Refusing the exhaustion at the value
// is what keeps every seq arithmetic in this package free of wraparound.
//
// DECISION: the range [1, MaxUint64-1]. The design gives the counter's
// formula without the bound it implies; this is that bound, stated so a
// store's "next_seq" column and this package agree on where a ledger ends.
const MaxLedgerSeq = math.MaxUint64 - 1

// LedgerEventSpec is the input to NewLedgerEvent: every member of an event
// except the hash, which is computed — or, when Hash is supplied, verified.
type LedgerEventSpec struct {
	// Seq is the event's position in the single-row sequence, in
	// [1, MaxLedgerSeq].
	Seq uint64
	// Kind is the event kind.
	Kind LedgerEventKind
	// OpID is the durable operation id (32 lowercase hex digits) the event
	// belongs to; one operation may append several events.
	OpID string
	// Path is the canonical path of the Resource the event concerns; kinds
	// update, allocate and tombstone.
	Path string
	// ScopeURL is the Scope URL the event names; kinds mint, rotate and
	// refuse_url.
	ScopeURL string
	// ResourceKind accompanies Path.
	ResourceKind ResourceKind
	// Revision is the revision the event minted or records; required for
	// update, optional for allocate and tombstone.
	Revision Revision
	// State is the tombstone's allocation state: pruned or erased.
	State AllocationState
	// Fingerprint is the installed descriptor's fingerprint (64 lowercase hex
	// digits); kind install.
	Fingerprint string
	// AuthorityID and Epoch stamp the authority that appended the event.
	AuthorityID string
	Epoch       uint64
	// At is when the event was appended; canonicalized to UTC microseconds.
	At time.Time
	// PrevHash is the previous event's hash, or GenesisHash for the first.
	PrevHash string
	// Hash, when nonempty, is the stored hash and must equal the computed one.
	Hash string
}

// ledgerShape is the per-kind member table: which optional members a kind
// requires and which it may carry. Members not listed are forbidden.
//
// DECISION: the schema says "CHECK per kind" without spelling the checks; this
// table is the spelling, chosen so that every member an event carries is one
// its kind has a meaning for. The hash layout does not depend on it — absent
// members are omitted whatever the kind — so a later ruling that widens or
// narrows a row changes validation, not any stored hash.
type ledgerShape struct {
	required, allowed ledgerMembers
}

type ledgerMembers struct{ path, scopeURL, resourceKind, revision, state, fingerprint bool }

var ledgerShapes = map[LedgerEventKind]ledgerShape{
	LedgerMint:      {required: ledgerMembers{scopeURL: true}, allowed: ledgerMembers{scopeURL: true}},
	LedgerInstall:   {required: ledgerMembers{fingerprint: true}, allowed: ledgerMembers{fingerprint: true}},
	LedgerUpdate:    {required: ledgerMembers{path: true, resourceKind: true, revision: true}, allowed: ledgerMembers{path: true, resourceKind: true, revision: true}},
	LedgerPromote:   {},
	LedgerRotate:    {required: ledgerMembers{scopeURL: true}, allowed: ledgerMembers{scopeURL: true}},
	LedgerRefuseURL: {required: ledgerMembers{scopeURL: true}, allowed: ledgerMembers{scopeURL: true}},
	LedgerAllocate:  {required: ledgerMembers{path: true, resourceKind: true}, allowed: ledgerMembers{path: true, resourceKind: true, revision: true}},
	LedgerTombstone: {required: ledgerMembers{path: true, resourceKind: true, state: true}, allowed: ledgerMembers{path: true, resourceKind: true, state: true, revision: true}},
}

// LedgerEvent is one append-only, hash-chained ledger event. Its hash is
// SHA-256 over the canonical bytes LedgerEventCanonicalBytes documents; the
// value is built only by NewLedgerEvent, so an event that exists has a hash
// that verifies.
type LedgerEvent struct {
	spec      LedgerEventSpec
	canonical []byte
}

// NewLedgerEvent validates the spec against its kind's shape, computes the
// canonical bytes and the hash, and — when spec.Hash was supplied — refuses
// the event if the stored hash does not match the computed one, which is how
// a tampered or corrupted stored event is detected on the way back in.
func NewLedgerEvent(spec LedgerEventSpec) (LedgerEvent, error) {
	shape, ok := ledgerShapes[spec.Kind]
	if !ok {
		return LedgerEvent{}, fmt.Errorf("%w: ledger event seq %d: unknown kind %q", ErrValidation, spec.Seq, spec.Kind)
	}
	fail := func(format string, args ...any) (LedgerEvent, error) {
		return LedgerEvent{}, fmt.Errorf("%w: ledger event seq %d (%s): %s", ErrValidation, spec.Seq, spec.Kind, fmt.Sprintf(format, args...))
	}
	if spec.Seq == 0 || spec.Seq > MaxLedgerSeq {
		return fail("seq must be in 1..%d (the ledger is exhausted beyond MaxLedgerSeq)", uint64(MaxLedgerSeq))
	}
	if !isLowerHex(spec.OpID, 32) {
		return fail("op_id must be 32 lowercase hex digits")
	}
	if !isLowerHex(spec.AuthorityID, 32) {
		return fail("authority_id must be 32 lowercase hex digits")
	}
	if spec.At.IsZero() {
		return fail("at is required")
	}
	if !isLowerHex(spec.PrevHash, 64) {
		return fail("prev_hash must be 64 lowercase hex digits")
	}
	present := ledgerMembers{
		path:         spec.Path != "",
		scopeURL:     spec.ScopeURL != "",
		resourceKind: spec.ResourceKind != "",
		revision:     !spec.Revision.IsZero(),
		state:        spec.State != "",
		fingerprint:  spec.Fingerprint != "",
	}
	for _, m := range []struct {
		name                       string
		present, required, allowed bool
	}{
		{"path", present.path, shape.required.path, shape.allowed.path},
		{"scope_url", present.scopeURL, shape.required.scopeURL, shape.allowed.scopeURL},
		{"resource_kind", present.resourceKind, shape.required.resourceKind, shape.allowed.resourceKind},
		{"revision", present.revision, shape.required.revision, shape.allowed.revision},
		{"state", present.state, shape.required.state, shape.allowed.state},
		{"fingerprint", present.fingerprint, shape.required.fingerprint, shape.allowed.fingerprint},
	} {
		if m.required && !m.present {
			return fail("%s is required", m.name)
		}
		if m.present && !m.allowed {
			return fail("%s is not a member of this kind", m.name)
		}
	}
	if present.scopeURL {
		// A minted, rotated-to or refused URL is a persisted identity.
		if err := ValidatePersistedScopeURL(spec.ScopeURL); err != nil {
			return fail("scope_url: %s", reason(err))
		}
	}
	if present.resourceKind && !spec.ResourceKind.Valid() {
		return fail("resource_kind must be bead or link")
	}
	if present.path {
		if err := ValidatePath(spec.Path, spec.ResourceKind); err != nil {
			return fail("path: %s", reason(err))
		}
	}
	if present.state && spec.State != AllocationPruned && spec.State != AllocationErased {
		return fail("state must be pruned or erased")
	}
	if present.fingerprint && !isLowerHex(spec.Fingerprint, 64) {
		return fail("fingerprint must be 64 lowercase hex digits")
	}
	spec.At = spec.At.UTC().Truncate(time.Microsecond)
	canonical := ledgerEventCanonicalBytes(spec)
	hash := hashHex(canonical)
	if spec.Hash != "" && spec.Hash != hash {
		return fail("stored hash %s does not verify (computed %s)", spec.Hash, hash)
	}
	spec.Hash = hash
	return LedgerEvent{spec: spec, canonical: canonical}, nil
}

// Spec returns a copy of the event's members, hash included — what a
// snapshot writes and a table row stores.
func (e LedgerEvent) Spec() LedgerEventSpec { return e.spec }

// Seq is the event's sequence number.
func (e LedgerEvent) Seq() uint64 { return e.spec.Seq }

// Kind is the event kind.
func (e LedgerEvent) Kind() LedgerEventKind { return e.spec.Kind }

// OpID is the operation id.
func (e LedgerEvent) OpID() string { return e.spec.OpID }

// Path is the Resource path, or "" for kinds that carry none.
func (e LedgerEvent) Path() string { return e.spec.Path }

// ScopeURL is the Scope URL, or "" for kinds that carry none.
func (e LedgerEvent) ScopeURL() string { return e.spec.ScopeURL }

// ResourceKind accompanies Path.
func (e LedgerEvent) ResourceKind() ResourceKind { return e.spec.ResourceKind }

// Revision is the revision carried, or the zero revision.
func (e LedgerEvent) Revision() Revision { return e.spec.Revision }

// State is the tombstone state, or "".
func (e LedgerEvent) State() AllocationState { return e.spec.State }

// Fingerprint is the installed fingerprint, or "".
func (e LedgerEvent) Fingerprint() string { return e.spec.Fingerprint }

// AuthorityID is the appending authority's id.
func (e LedgerEvent) AuthorityID() string { return e.spec.AuthorityID }

// Epoch is the appending authority's epoch.
func (e LedgerEvent) Epoch() uint64 { return e.spec.Epoch }

// At is when the event was appended, in UTC at microsecond precision.
func (e LedgerEvent) At() time.Time { return e.spec.At }

// PrevHash is the previous event's hash, or GenesisHash.
func (e LedgerEvent) PrevHash() string { return e.spec.PrevHash }

// Hash is the event's hash.
func (e LedgerEvent) Hash() string { return e.spec.Hash }

// CanonicalBytes returns a copy of the bytes the hash covers.
func (e LedgerEvent) CanonicalBytes() []byte { return append([]byte(nil), e.canonical...) }

// IsZero reports an event no constructor produced.
func (e LedgerEvent) IsZero() bool { return e.spec.Hash == "" }

// LedgerManifest describes one contiguous range of a Scope's ledger, as the
// ledger lane snapshots and applies it: the Scope URL, the authority lineage,
// the range, the hash the range continues from, and the range's head hash.
//
// DECISION: "authority lineage" is the hash of the Scope's mint event. The
// design names the member without defining it; the mint event's hash covers
// the Scope URL, the minting authority id and epoch, the operation id and the
// instant, and is the value ruling 14's "earlier mint wins" compares — so it
// is the lineage in the only sense the ledger can verify.
type LedgerManifest struct {
	scopeURL string
	lineage  string
	firstSeq uint64
	lastSeq  uint64
	prevHash string
	headHash string
}

// LedgerManifestSpec is the input to NewLedgerManifest.
type LedgerManifestSpec struct {
	// ScopeURL is the Scope the ledger belongs to.
	ScopeURL string
	// Lineage is the mint event's hash.
	Lineage string
	// FirstSeq and LastSeq bound the range, inclusive, within
	// [1, MaxLedgerSeq].
	FirstSeq, LastSeq uint64
	// PrevHash is the hash the first event of the range links to: GenesisHash
	// when the range starts at the mint event.
	PrevHash string
	// HeadHash is the last event's hash.
	HeadHash string
}

// NewLedgerManifest validates the manifest's own shape; Covers checks it
// against the events it claims to describe.
func NewLedgerManifest(spec LedgerManifestSpec) (LedgerManifest, error) {
	if err := ValidatePersistedScopeURL(spec.ScopeURL); err != nil {
		return LedgerManifest{}, err
	}
	for _, h := range []struct{ name, value string }{
		{"lineage", spec.Lineage}, {"prev_hash", spec.PrevHash}, {"head_hash", spec.HeadHash},
	} {
		if !isLowerHex(h.value, 64) {
			return LedgerManifest{}, fmt.Errorf("%w: ledger manifest %s must be 64 lowercase hex digits", ErrValidation, h.name)
		}
	}
	if spec.FirstSeq == 0 {
		return LedgerManifest{}, fmt.Errorf("%w: ledger manifest range must start at seq 1 or later (0 is not an event)", ErrValidation)
	}
	if spec.LastSeq > MaxLedgerSeq {
		return LedgerManifest{}, fmt.Errorf("%w: ledger manifest range ends at seq %d, beyond MaxLedgerSeq (the sequence is exhausted)", ErrValidation, spec.LastSeq)
	}
	if spec.LastSeq < spec.FirstSeq {
		return LedgerManifest{}, fmt.Errorf("%w: ledger manifest range %d..%d is empty", ErrValidation, spec.FirstSeq, spec.LastSeq)
	}
	return LedgerManifest{
		scopeURL: spec.ScopeURL,
		lineage:  spec.Lineage,
		firstSeq: spec.FirstSeq,
		lastSeq:  spec.LastSeq,
		prevHash: spec.PrevHash,
		headHash: spec.HeadHash,
	}, nil
}

// ScopeURL is the Scope the ledger belongs to.
func (m LedgerManifest) ScopeURL() string { return m.scopeURL }

// Lineage is the mint event's hash.
func (m LedgerManifest) Lineage() string { return m.lineage }

// FirstSeq is the first sequence number of the range.
func (m LedgerManifest) FirstSeq() uint64 { return m.firstSeq }

// LastSeq is the last sequence number of the range.
func (m LedgerManifest) LastSeq() uint64 { return m.lastSeq }

// PrevHash is the hash the range continues from.
func (m LedgerManifest) PrevHash() string { return m.prevHash }

// HeadHash is the range's head hash.
func (m LedgerManifest) HeadHash() string { return m.headHash }

// IsZero reports a manifest no constructor produced.
func (m LedgerManifest) IsZero() bool { return m.scopeURL == "" }

// Covers is the pure half of LedgerApply's recovery predicate: the events are
// exactly the range the manifest describes, contiguous, chained from PrevHash
// to HeadHash, each hash verifying, and — when the range starts at the origin
// — the first event is the mint event whose hash is the lineage. The store's
// half (lineage matches the Scope row; the store's head equals PrevHash) is
// the implementation's.
func (m LedgerManifest) Covers(events []LedgerEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("%w: ledger manifest covers seq %d..%d, no events supplied", ErrValidation, m.firstSeq, m.lastSeq)
	}
	// firstSeq ≤ lastSeq ≤ MaxLedgerSeq (NewLedgerManifest), so the span
	// and the span plus one both fit.
	if want := m.lastSeq - m.firstSeq + 1; uint64(len(events)) != want {
		return fmt.Errorf("%w: ledger manifest covers %d events, %d supplied", ErrValidation, want, len(events))
	}
	if events[0].Seq() != m.firstSeq {
		return fmt.Errorf("%w: ledger manifest starts at seq %d, events start at %d", ErrValidation, m.firstSeq, events[0].Seq())
	}
	if err := VerifyLedgerChain(m.prevHash, events); err != nil {
		return err
	}
	if head := events[len(events)-1].Hash(); head != m.headHash {
		return fmt.Errorf("%w: ledger manifest head %s, events end at %s", ErrValidation, m.headHash, head)
	}
	if m.prevHash == GenesisHash {
		if events[0].Kind() != LedgerMint {
			return fmt.Errorf("%w: a ledger range from genesis must begin with the mint event", ErrValidation)
		}
		if events[0].Hash() != m.lineage {
			return fmt.Errorf("%w: ledger manifest lineage %s is not the mint event's hash %s", ErrValidation, m.lineage, events[0].Hash())
		}
	}
	return nil
}

// OwnedLinkGroup is one ownedLinks entry: the owned Link Type and the owned
// Links of one Bead under it, complete records in code-unit order of path.
// The key is always a Link Type URL, never WildcardOwnedLinkKey. An
// explicitly declared Type with no Links is an EMPTY group, never an absent
// one; a Type owned only through the wildcard has a group exactly when the
// Bead has a Link of it (bdp#1 item 5: "one entry per owned type actually
// present, plus an empty entry for each explicitly declared type"). An
// explicit group holds at most its declaration's Max Links; under a
// wildcard declaration the Links of ALL of a Bead's groups together —
// explicit groups included — number at most the wildcard's Max (OW1 = A).
type OwnedLinkGroup struct {
	TypeURL string
	Links   []Link
}

// BeadRecord is a Bead with its complete ownedLinks expansion, assembled in
// the same snapshot: one group per Link Type the Bead's Type declares
// explicitly, empty groups included, plus — under a wildcard declaration —
// one group per wildcard-owned Link Type the Bead actually has Links of, all
// in code-unit order of TypeURL; nil when nothing is owned or present. Every
// projection — singleton, collection item, selection item — returns records,
// because the Bead's revision covers its owned Links. The expansion is
// bounded, which is what makes it servable inline: each explicit group by
// its declaration's Max, and — under a wildcard — the whole expansion, every
// group together, by the wildcard's Max (OW1 = A), the one bound the store's
// batched owned-Links read enforces with LIMIT max + 1 and the one
// CheckBeadRecord refuses a record for exceeding.
type BeadRecord struct {
	Bead       Bead
	OwnedLinks []OwnedLinkGroup
}

// CheckBeadRecord verifies a record against the owned-Link declarations of the
// Bead's Type (its OwnsOutgoing, in any order): the groups ascend in code-unit
// order of Link Type URL with no repeats and none keyed by the wildcard; there
// is exactly one group, possibly empty, per explicit declaration; a group for
// any other Type exists only under a wildcard declaration and only when it
// holds a Link; in every group each Link's Type equals the group key, its
// source is the Bead, and the Links ascend in code-unit order of path with no
// repeats; and the record is within its bounds — an explicit group holds at
// most its declaration's Max Links, and under a wildcard declaration the
// Links of all groups together number at most the wildcard's Max, the whole
// owned set (OW1 = A). It is the acceptance law the plan states and the
// storage transactions run.
//
// DECISION: the bounds ARE checked here, both of them. Before OW1 = A this
// law left Max to the serving transaction's LIMIT max + 1 — "not a fact
// about an assembled record" — and the ruling made the wildcard's Max
// exactly such a fact: the whole-set bound is what a Bead's inline owned
// plane may hold, so a record over it is one the descriptor says cannot
// exist, and serving it would serve a contract violation. The store's
// batched owned-Links read for a wildcard owner selects every owned Link of
// the Bead, all Types together, under LIMIT wildcard.max + 1 (B4) and
// refuses to assemble when the extra row comes back; this law is the pure
// restatement of that same bound over what was assembled, so the two never
// disagree. The explicit per-Type bound is checked too, for consistency —
// one acceptance law whether or not a wildcard is present, rather than a
// Max that is a fact under a wildcard and a mere LIMIT hint without one —
// and because it is the conservative choice: dropping a check later widens
// what is accepted, adding one later would refuse records that once passed.
// The declarations are a descriptor's own (no explicit Max exceeds the
// wildcard's there), so the two checks are independent and either may be
// the one that fires.
func CheckBeadRecord(record BeadRecord, owns []OwnedLinkDecl) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: bead %s: %s", ErrValidation, record.Bead.Path(), fmt.Sprintf(format, args...))
	}
	var explicit []OwnedLinkDecl
	var wildcard OwnedLinkDecl // the zero declaration when there is none
	for _, decl := range owns {
		if decl.Wildcard() {
			wildcard = decl
			continue
		}
		explicit = append(explicit, decl)
	}
	sort.SliceStable(explicit, func(i, j int) bool {
		return CompareCodeUnits(explicit[i].typeURL, explicit[j].typeURL) < 0
	})
	next := 0  // the first explicit declaration not yet matched by a group
	total := 0 // owned Links across all groups: the whole owned set
	for i, group := range record.OwnedLinks {
		if group.TypeURL == WildcardOwnedLinkKey {
			return fail("ownedLinks group %d is keyed by the wildcard; groups are keyed by Link Type URL", i)
		}
		if i > 0 && CompareCodeUnits(record.OwnedLinks[i-1].TypeURL, group.TypeURL) >= 0 {
			return fail("ownedLinks groups are not in ascending code-unit order at %s", group.TypeURL)
		}
		// Groups ascend, so an explicit declaration below this key that is
		// still unmatched has no group at all.
		if next < len(explicit) && CompareCodeUnits(explicit[next].typeURL, group.TypeURL) < 0 {
			return fail("no ownedLinks group for explicitly owned %s", explicit[next].typeURL)
		}
		switch {
		case next < len(explicit) && explicit[next].typeURL == group.TypeURL:
			// The explicit declaration's group, empty or not, within its
			// own per-Type bound.
			if len(group.Links) > explicit[next].max {
				return fail("ownedLinks group %s holds %d Links, over its declared max %d", group.TypeURL, len(group.Links), explicit[next].max)
			}
			next++
		case !wildcard.Wildcard():
			return fail("ownedLinks group %s is not an owned Type", group.TypeURL)
		case len(group.Links) == 0:
			return fail("empty ownedLinks group %s: a wildcard-owned Type has a group only when a Link is present", group.TypeURL)
		}
		total += len(group.Links)
		for j, link := range group.Links {
			if link.TypeURL() != group.TypeURL {
				return fail("owned Link %s has type %s under group %s", link.Path(), link.TypeURL(), group.TypeURL)
			}
			if !link.Source().InScope() || link.Source().Path() != record.Bead.Path() {
				return fail("owned Link %s has another source", link.Path())
			}
			if j > 0 && CompareCodeUnits(group.Links[j-1].Path(), link.Path()) >= 0 {
				return fail("owned Links under %s are not in ascending code-unit order", group.TypeURL)
			}
		}
	}
	if next < len(explicit) {
		return fail("no ownedLinks group for explicitly owned %s", explicit[next].typeURL)
	}
	if wildcard.Wildcard() && total > wildcard.max {
		return fail("%d owned Links across all groups, over the wildcard's whole-set max %d", total, wildcard.max)
	}
	return nil
}

// BeadPage is one page of BeadRecords, in code-unit order of path, and the
// cursor that continues it ("" after the last page).
type BeadPage struct {
	Items []BeadRecord
	Next  Cursor
}

// LinkPage is one page of Links, in code-unit order of path, and the cursor
// that continues it ("" after the last page).
type LinkPage struct {
	Items []Link
	Next  Cursor
}
