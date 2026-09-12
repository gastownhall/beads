package bdpwire

import "fmt"

// BDPVersion is the only value `bdpVersion` takes for a BDP v0 Scope (bundle
// `bdpVersion`: const "0"). It names the complete protocol version, not a
// feature level: a client that does not implement the advertised value stops
// rather than guessing compatibility.
const BDPVersion = "0"

// ReadDiscovery is the `readDiscovery` envelope: the machine-readable
// discovery document a Read-profile Scope serves at the ServiceDescRel link
// of its Scope URL. Membership is profile-specific and this type is the Read
// column of the spec's table: `operations` and every Transactional member are
// prohibited in Read, so a strict decode of a higher-profile document into
// this type fails on them by construction (roundtrip_test.go proves it with
// the spec's own Read+Update and Transactional examples).
type ReadDiscovery struct {
	// BDPVersion is BDPVersion ("0") for every v0 Scope.
	BDPVersion string `json:"bdpVersion"`
	// Profile is the Scope's highest supported cumulative profile; here it is
	// always ProfileRead (the bundle pins it to that const).
	Profile ProtocolProfile `json:"profile"`
	// Scope is the canonical Scope URL, ending in "/": the Scope's identity
	// and the base every local ID and durable relative reference resolves
	// against.
	Scope string `json:"scope"`
	// Beads, Links and Types are the fixed roots — exactly "beads/", "links/"
	// and "types/" resolved against Scope — and the only top-level paths under
	// which this Scope assigns Bead, Link and Type semantics.
	Beads string `json:"beads"`
	Links string `json:"links"`
	Types string `json:"types"`
	// Aliases is present exactly when the authority serves alias resolution
	// ("alias/" resolved against Scope). A client MUST NOT construct alias
	// URLs for an authority that does not advertise it.
	Aliases string `json:"aliases,omitempty"`
	// Order names the collection order; absent means the OrderCanonicalURI
	// baseline.
	Order CollectionOrder `json:"order,omitempty"`
	// Limits pre-advertises operational bounds. Omission means only that a
	// bound is not pre-advertised — not infinite capacity, and never a
	// license to truncate silently.
	Limits *AdvertisedLimits `json:"limits,omitempty"`
	// MaximumEndpointMultiplicity is the Scope's unordered aggregate policy;
	// absent or empty means no such policy, which is why omitempty is
	// lossless here.
	MaximumEndpointMultiplicity []MaximumEndpointMultiplicityPolicy `json:"maximumEndpointMultiplicity,omitempty"`
}

// Validate checks the members the bundle pins to one value: bdpVersion is
// BDPVersion ("0") — a client that does not implement the advertised
// version stops rather than guessing — and profile is ProfileRead, the only
// profile a Read discovery document may advertise; and, when order is
// present, that it names a defined collection order. These are structural
// constants the bundle states about the bytes, so they are checked here and
// not left to a graph law (doc.go); decoding alone settles the shape.
func (d ReadDiscovery) Validate() error {
	if d.BDPVersion != BDPVersion {
		return fmt.Errorf("bdpwire: discovery bdpVersion %q, want %q", d.BDPVersion, BDPVersion)
	}
	if d.Profile != ProfileRead {
		return fmt.Errorf("bdpwire: discovery profile %q, want %q for a Read discovery document", d.Profile, ProfileRead)
	}
	if d.Order != "" && !d.Order.Valid() {
		return fmt.Errorf("bdpwire: discovery order %q is not a defined collection order", d.Order)
	}
	return nil
}

// AdvertisedLimits is the `advertisedLimits` envelope: the optional `limits`
// object of discovery, divided into capability groups. A group is relevant
// only when the advertised profile exposes that capability, and every
// advertised value is binding. DECISION: counts are int and durations are
// string, each with omitempty, rather than pointers — the bundle's
// positiveInteger has minimum 1 and its duration pattern forbids the empty
// string, so a zero count and an empty duration can only mean "not
// advertised" and nothing is lost.
type AdvertisedLimits struct {
	Page        *PageLimits        `json:"page,omitempty"`
	Request     *RequestLimits     `json:"request,omitempty"`
	Resource    *ResourceLimits    `json:"resource,omitempty"`
	Selector    *SelectorLimits    `json:"selector,omitempty"`
	Patch       *PatchLimits       `json:"patch,omitempty"`
	Sequence    *SequenceLimits    `json:"sequence,omitempty"`
	Transaction *TransactionLimits `json:"transaction,omitempty"`
	Retention   *RetentionLimits   `json:"retention,omitempty"`
}

// PageLimits counts Resource records per page.
type PageLimits struct {
	DefaultItems int `json:"defaultItems,omitempty"`
	MaximumItems int `json:"maximumItems,omitempty"`
}

// RequestLimits counts octets in the encoded HTTP request target and in the
// representation body.
type RequestLimits struct {
	TargetBytes int `json:"targetBytes,omitempty"`
	BodyBytes   int `json:"bodyBytes,omitempty"`
}

// ResourceLimits counts UTF-8 bytes in the corresponding JSON serialization.
type ResourceLimits struct {
	RepresentationBytes int `json:"representationBytes,omitempty"`
	PropertiesBytes     int `json:"propertiesBytes,omitempty"`
}

// SelectorLimits bound one Selector: Bytes after percent-decoding, Depth and
// Nodes over the parsed structure.
type SelectorLimits struct {
	Bytes int `json:"bytes,omitempty"`
	Depth int `json:"depth,omitempty"`
	Nodes int `json:"nodes,omitempty"`
}

// PatchLimits bound one property patch (a later-profile capability; the group
// is part of the shared definition).
type PatchLimits struct {
	Operations int `json:"operations,omitempty"`
	PathBytes  int `json:"pathBytes,omitempty"`
	PathDepth  int `json:"pathDepth,omitempty"`
}

// SequenceLimits bound members in one Read+Update sequence.
type SequenceLimits struct {
	Operations int `json:"operations,omitempty"`
}

// TransactionLimits bound one Transactional Mutation Transaction; Duration is
// an ISO 8601 duration, the rest are counts.
type TransactionLimits struct {
	Operations        int    `json:"operations,omitempty"`
	ExaminedResources int    `json:"examinedResources,omitempty"`
	MatchedResources  int    `json:"matchedResources,omitempty"`
	MutatedResources  int    `json:"mutatedResources,omitempty"`
	InducedEvents     int    `json:"inducedEvents,omitempty"`
	Duration          string `json:"duration,omitempty"`
}

// RetentionLimits are ISO 8601 durations. MaximumSnapshotLifetime is the one
// the Read profile uses (the pinned matrix reads it as the cursor lifetime).
type RetentionLimits struct {
	Idempotency             string `json:"idempotency,omitempty"`
	Receipt                 string `json:"receipt,omitempty"`
	MaximumSnapshotLifetime string `json:"maximumSnapshotLifetime,omitempty"`
	Replay                  string `json:"replay,omitempty"`
}

// MaximumEndpointMultiplicityPolicy is the `maximumEndpointMultiplicityPolicy`
// envelope: at most Max Links whose effective Type set contains
// LinkConformsTo may share one Bead at Endpoint. It is Scope-owned aggregate
// policy, not a Type Descriptor member, because checking it means inspecting
// other Links.
type MaximumEndpointMultiplicityPolicy struct {
	LinkConformsTo string   `json:"linkConformsTo"`
	Endpoint       Endpoint `json:"endpoint"`
	Max            int      `json:"max"`
}

// BeadRecord is the `beadRecord` envelope: the self-contained record every
// successful GET of a Bead returns and every Bead collection page embeds
// directly. ID and Type are absolute canonical URLs in responses; Revision is
// protocol metadata, opaque and equality-only, and so is Attribution — both
// sit beside Properties, never inside it.
type BeadRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Revision string `json:"revision"`
	// Attribution is the per-version carried attribution — data, not
	// evidence — and absent when none was recorded.
	Attribution *Attribution `json:"attribution,omitempty"`
	// Properties is the complete stored properties document, declared and
	// undeclared members alike; it is never a schema-filtered projection.
	Properties Properties `json:"properties"`
	// Links is present only on the `include=links` aggregate: the first page
	// of the same result the Bead's `view=links` exposes. The default GET is
	// bounded and never carries it.
	Links *LinkCollection `json:"links,omitempty"`
	// OwnedLinks is present exactly when the Bead's Type owns outgoing Link
	// Types: one entry per declared owned Link Type, keyed by the Link Type
	// URL, valued by the owned Links' complete records in ascending code-unit
	// order of their canonical ids — derived data covered by Revision, never
	// writable directly. An entry is present, possibly empty, for every
	// declared owned Type, so an owning Type never yields an empty map and
	// omitempty is lossless.
	OwnedLinks OwnedLinks `json:"ownedLinks,omitempty"`
}

// LinkRecord is the `linkRecord` envelope: a first-class directed
// relationship with its own identity, Type, revision and properties. Source
// and Target are References — an in-Scope endpoint's URI is the Bead's
// absolute canonical URL, an out-of-Scope endpoint's is its opaque absolute
// URI, and either may be pinned. ID, Type, Source and Target are immutable:
// repointing or re-pinning is a delete-and-create pair.
type LinkRecord struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Revision    string       `json:"revision"`
	Attribution *Attribution `json:"attribution,omitempty"`
	Source      Reference    `json:"source"`
	Target      Reference    `json:"target"`
	Properties  Properties   `json:"properties"`
}

// Attribution is the `attribution` envelope carried per version on a Bead or
// Link record. Principal is a nonempty opaque string — SHOULD be namespaced
// (`agent:…`, `human:…`, `svc:…`) and is compared only for byte equality —
// and Status records the realization's basis for it, never a BDP guarantee.
type Attribution struct {
	Principal string            `json:"principal"`
	Status    AttributionStatus `json:"status"`
}

// BeadCollection is the `beadCollection` envelope: one page of complete Bead
// records in the authority's advertised total order, and the authoritative
// continuation. Next is null after the final page, which is why it is a
// pointer without omitempty: the member is required and its absence and its
// null mean different things.
type BeadCollection struct {
	Items BeadRecords `json:"items"`
	Next  *string     `json:"next"`
}

// LinkCollection is the `linkCollection` envelope: one page of complete Link
// records — a `links/` page, a Bead's `view=links` page, or the page embedded
// by `include=links`.
type LinkCollection struct {
	Items LinkRecords `json:"items"`
	Next  *string     `json:"next"`
}

// TypesInventory is the `typesInventory` envelope: one page of the `types/`
// inventory, each item exactly a TypeSummary. It says which Types the Scope
// knows; it is not a closed-world claim.
type TypesInventory struct {
	Items TypeSummaries `json:"items"`
	Next  *string       `json:"next"`
}

// TypeSummary is the `typeSummary` envelope, an inventory entry: ID is the
// Type Descriptor URL, which may be hosted inside or outside the Scope and
// names the complete contract.
type TypeSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Describes Describes `json:"describes"`
}

// TypeDescriptor is the `typeDescriptor` envelope: the self-contained JSON
// descriptor served at a Type ID. Descriptor objects are closed. A Link Type
// carries Source and Target (both required by the bundle's conditional) and
// never OwnsOutgoing; a Bead Type carries neither endpoint constraint and may
// carry OwnsOutgoing. Those conditionals are Type-contract law, checked by
// the graph leaf's validators rather than by this type.
type TypeDescriptor struct {
	// ID is the absolute canonical Type ID Resources declare.
	ID string `json:"id"`
	// Name is required, nonempty, human-readable, and establishes nothing.
	Name string `json:"name"`
	// Description is optional human-readable documentation. DECISION: like
	// every optional string here (Label, PropertiesSchema, a problem's Title),
	// it is a plain string with omitempty — an explicit "" is served as
	// absent, which the bundle gives no way to tell apart in meaning.
	Description string `json:"description,omitempty"`
	// Describes is the Resource category and must agree with every Resource
	// declaring the Type.
	Describes Describes `json:"describes"`
	// ConformsTo lists the direct parent Type IDs, unordered, and is always an
	// array — TypeIDs marshals nil as [] for that reason.
	ConformsTo TypeIDs `json:"conformsTo"`
	// PropertiesSchema, when present, is the absolute URL of a JSON Schema
	// 2020-12 document for the Resource's `properties` object — not for its
	// generic BDP record.
	PropertiesSchema string `json:"propertiesSchema,omitempty"`
	// Source and Target are a Link Type's endpoint constraints.
	Source *EndpointConstraint `json:"source,omitempty"`
	Target *EndpointConstraint `json:"target,omitempty"`
	// OwnsOutgoing declares the outgoing Link Types a Bead Type owns, keyed
	// by owned Link Type URL plus the optional max-only wildcard. Explicit
	// pairs occur at most once; the wildcard bounds the whole owned set.
	OwnsOutgoing *OwnedOutgoingDeclarations `json:"ownsOutgoing,omitempty"`
}

// EndpointConstraint is the `endpointConstraint` envelope: the Types an
// in-Scope endpoint Bead must satisfy (every listed one is required; an empty
// list accepts any in-Scope Bead) and the policy for out-of-Scope references
// at that endpoint (absent means ExternalOpaque). Closed.
type EndpointConstraint struct {
	ConformsTo TypeIDs        `json:"conformsTo"`
	External   ExternalPolicy `json:"external,omitempty"`
}

// OwnedLinkDeclaration is the `ownedLinkDeclaration` envelope, one value of
// OwnsOutgoing: Max is the required bound on the owned set, and Label is
// display documentation that appears only in the descriptor and never in any
// Resource record.
type OwnedLinkDeclaration struct {
	Label string `json:"label,omitempty"`
	Max   int    `json:"max"`
}
