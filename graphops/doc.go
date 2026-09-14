// Package graphops is the PUBLIC LEAF of the bead graph: the immutable domain
// values a BDP Scope is made of, the laws those values obey, and the role
// interfaces every storage leg implements and every front door consumes.
//
// It is a sibling of issueops and memoryops at the repository root, and it is
// the third plane over the workspace: Issues and Dependencies are one graph,
// memories are a keyed namespace, and this is the BEAD GRAPH the Bead Protocol
// (BDP) serves — Beads and Links under Type Descriptors inside one Scope,
// with P0 wire input pinned to the merged Read foundation at 19923f5b
// (engdocs/BDP_BEAD_GRAPH_PLAN.md §0 preserves the original 0b7d86e7 input
// as history). Nothing here projects an Issue; the two planes join later.
//
// THREE LAYERS, STRICTLY SEPARATED. Values (types.go) carry unexported fields
// and are built only by constructors that enforce the laws, so a value that
// exists is a value that is valid: a path is canonical, a properties document
// is one canonical byte string, a Reference knows which side of the Scope it
// is on. Laws (laws.go) are pure functions — the canonical-ID grammar, the
// code-unit order every collection is served in, JSON canonicalization and the
// RFC 6902 §4.6 equality that decides whether a write is a no-op, the Scope URL
// contract, and the ledger's hash chain — stated once and table-tested once,
// so no storage leg and no front door owns a private copy. Roles (reader.go,
// types_role.go, identity.go) are the six questions a caller can ask of a
// store, designed to sit behind BeadGraph* accessors on storage.Storage in
// P1; a Writer is P3 and deliberately absent.
//
// AUTHORITY IS NEVER A PARAMETER. No request type in this package carries an
// authority id, an epoch, an installation key, a fence or a lease: the witness
// belongs to the store, is loaded by the accessor and asserted inside the
// transaction (engdocs/BDP_GRAPH_ARCHITECTURE.md §2b A1). A caller says what
// it wants — a path, a page, a descriptor — and the store decides whether it
// is entitled to answer. The identity role REPORTS the claim; nothing takes
// it as input.
//
// WHAT THIS PACKAGE IMPORTS: the standard library and
// github.com/steveyegge/beads/beadserrors, and nothing else — enforced by the
// graphops-leaf depguard rule in .golangci.yml and by a test here. Every
// storage leg, the future BDP handler and wire client, and cmd/bd are designed
// to consume this package; P0 does not install those consumers. Anything it
// imports would be imported by all of them, and the backend/ completeness
// guard would have to alias any internal/ type reachable from it. Wire DTOs
// are hand-written and checked against the pinned schema bundle in a separate
// package (bdpwire/GENERATOR.md); this package never sees them. Errors are
// ordinary typed Go errors, and the BDP handler — and only the handler — maps
// them to Problem records.
//
// Where the design documents or the pinned specification are silent or
// disagree, the code takes the most conservative reading and says so in a
// `// DECISION:` comment at the site, so the reader finds the choice where it
// bites rather than in a change log.
package graphops
