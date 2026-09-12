package bdpwire

// Every closed vocabulary the bundle defines, as a named string type with its
// members as constants and a Valid method, the shape internal/httpapi/apigen
// already uses for enums. Each set is welded to the bundle by
// schema_parity_test.go (two-way, member for member), so a value added or
// renamed upstream fails a test here rather than shipping as a silent string.

// ProtocolProfile is the bundle's `protocolProfile` vocabulary: the highest
// cumulative profile a Scope advertises. ReadDiscovery only ever carries
// ProfileRead — the bundle pins its `profile` member to that const — and the
// other two exist because the shared definition does, so a later-profile
// discovery type reuses them rather than minting twins.
type ProtocolProfile string

// The protocol profiles, lowest to highest; claiming a higher one claims every
// lower one.
const (
	ProfileRead          ProtocolProfile = "read"
	ProfileReadUpdate    ProtocolProfile = "read-update"
	ProfileTransactional ProtocolProfile = "transactional"
)

// Valid reports whether p is a member of the bundle's vocabulary.
func (p ProtocolProfile) Valid() bool {
	switch p {
	case ProfileRead, ProfileReadUpdate, ProfileTransactional:
		return true
	}
	return false
}

// RetryDisposition is the bundle's `retryDisposition` vocabulary: what a
// caller may do after a problem. Each ReadProblemCode fixes its disposition
// (ReadProblemCode.Retry); a problem never chooses one freely.
type RetryDisposition string

// The Read-profile retry dispositions. RetryAfterStateChange means refresh
// state or build a new request; RetryAfterDelay responses SHOULD carry
// Retry-After when the authority can state a useful delay.
const (
	RetryNever            RetryDisposition = "never"
	RetryAfterStateChange RetryDisposition = "after-state-change"
	RetryAfterDelay       RetryDisposition = "after-delay"
)

// Valid reports whether r is a member of the bundle's vocabulary.
func (r RetryDisposition) Valid() bool {
	switch r {
	case RetryNever, RetryAfterStateChange, RetryAfterDelay:
		return true
	}
	return false
}

// ReadProblemCode is the bundle's `readProblemCode` vocabulary: the closed
// Read-profile problem table. The code is the member a client dispatches on;
// its family, HTTP status and retry disposition are fixed per code and read
// back from the code (problem.go), never chosen per response.
type ReadProblemCode string

// The thirteen Read-profile problem codes. resource-pruned and resource-erased
// are the authorization-gated disclosure conditions: served only to a caller
// authorized for the subject's retained history, and otherwise the uniform
// resource-not-found, so 410 is never an enumeration oracle.
const (
	CodeMalformedRequest       ReadProblemCode = "malformed-request"
	CodeInvalidParameter       ReadProblemCode = "invalid-parameter"
	CodeUnauthenticated        ReadProblemCode = "unauthenticated"
	CodeForbidden              ReadProblemCode = "forbidden"
	CodeResourceNotFound       ReadProblemCode = "resource-not-found"
	CodeResourcePruned         ReadProblemCode = "resource-pruned"
	CodeResourceErased         ReadProblemCode = "resource-erased"
	CodeForeignView            ReadProblemCode = "foreign-view"
	CodeCursorExpired          ReadProblemCode = "cursor-expired"
	CodeRequestTooLarge        ReadProblemCode = "request-too-large"
	CodeLimitExceeded          ReadProblemCode = "limit-exceeded"
	CodeRateLimited            ReadProblemCode = "rate-limited"
	CodeTemporarilyUnavailable ReadProblemCode = "temporarily-unavailable"
)

// Valid reports whether c is a member of the Read problem table.
func (c ReadProblemCode) Valid() bool {
	_, ok := readProblemTable[c]
	return ok
}

// Describes is a Type Descriptor's Resource category: a Type describes Beads
// or Links, never both, and must agree with every Resource declaring it.
type Describes string

// The two Resource categories.
const (
	DescribesBead Describes = "bead"
	DescribesLink Describes = "link"
)

// Valid reports whether d is a member of the bundle's vocabulary.
func (d Describes) Valid() bool {
	switch d {
	case DescribesBead, DescribesLink:
		return true
	}
	return false
}

// ExternalPolicy is an endpoint constraint's external-endpoint policy: what
// an out-of-Scope reference at that endpoint may be when a Link is created.
// An absent member means ExternalOpaque.
type ExternalPolicy string

// The external-endpoint policies. ExternalBead admits an external URI only
// when it is bead-shaped (a canonical HTTP(S) URL whose path carries a
// `beads/{id}` tail); it is declared intent about creation time, never an
// ongoing guarantee, and the authority never dereferences it to find out.
const (
	ExternalNone   ExternalPolicy = "none"
	ExternalOpaque ExternalPolicy = "opaque"
	ExternalBead   ExternalPolicy = "bead"
)

// Valid reports whether e is a member of the bundle's vocabulary.
func (e ExternalPolicy) Valid() bool {
	switch e {
	case ExternalNone, ExternalOpaque, ExternalBead:
		return true
	}
	return false
}

// Endpoint names one end of a Link, as a maximum-multiplicity policy does.
type Endpoint string

// The two Link endpoints.
const (
	EndpointSource Endpoint = "source"
	EndpointTarget Endpoint = "target"
)

// Valid reports whether e is a member of the bundle's vocabulary.
func (e Endpoint) Valid() bool {
	switch e {
	case EndpointSource, EndpointTarget:
		return true
	}
	return false
}

// AttributionStatus is the basis a realization records for a carried
// attribution's principal. It is data, not evidence: there is deliberately no
// status asserting the authority verified the principal, because that would
// be an authority claim, which this member never carries.
type AttributionStatus string

// The two v0 statuses: AttributionClaimed — the writer of that version
// supplied the principal, as written; AttributionUnknown — the principal is
// carried from data whose relationship to this version the realization cannot
// establish (an imported record, say).
const (
	AttributionClaimed AttributionStatus = "claimed"
	AttributionUnknown AttributionStatus = "unknown"
)

// Valid reports whether s is a member of the bundle's vocabulary.
func (s AttributionStatus) Valid() bool {
	switch s {
	case AttributionClaimed, AttributionUnknown:
		return true
	}
	return false
}

// CollectionOrder is the total order an authority serves every collection
// response in. The baseline every authority MUST support is OrderCanonicalURI:
// ascending comparison, by Unicode code unit, of each item's absolute
// canonical `id`. An authority advertising no `order` serves the baseline, and
// MUST NOT serve an order it does not advertise.
type CollectionOrder string

// OrderCanonicalURI is the only order BDP v0 defines.
const OrderCanonicalURI CollectionOrder = "canonical-uri"

// Valid reports whether o is a member of the bundle's vocabulary.
func (o CollectionOrder) Valid() bool {
	return o == OrderCanonicalURI
}
