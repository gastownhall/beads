// Package beadserrors holds the error vocabulary that is shared by every role
// leaf rather than owned by any one of them.
//
// It exists because the second leaf proved the first one could not keep owning
// it. memoryops needs to classify a validation refusal, and the value the whole
// repo matches lived in issueops — so the memory plane imported the issue
// package for one errors.New, and dragged internal/types in behind it. That
// import claimed memory is downstream of issues. It is not: the two are sibling
// planes over one config table.
//
// WHAT BELONGS HERE is the vocabulary any role needs whatever it operates on: a
// request was invalid, a thing was not there, the substrate was not ready, this
// backend does not implement the capability. WHAT DOES NOT is every refusal
// that names a domain concept — an issue cannot be claimed, a close is blocked,
// a dependency would cycle. Those stay in the leaf that defines the concept.
// The test is whether a leaf for some plane nobody has written yet would need
// it; if the answer requires knowing what the plane holds, it is not shared.
//
// Leaves RE-EXPORT from here rather than making callers import it, so code
// holding one role interface can classify a refusal without discovering a
// second package. Those re-exports are Go aliases, so every value is identical
// and one errors.Is arm matches it under any of its names — which is the whole
// reason to alias instead of minting per-package twins. issueops.ErrValidation,
// storage.ErrValidation, backend.ErrValidation and memoryops.ErrValidation are
// four doorplates on the one value declared below.
//
// The package imports stdlib and nothing else, and it must stay that way: it
// sits beneath every leaf, so anything it imports is imported by all of them.
package beadserrors

import (
	"errors"
	"fmt"
)

// ErrValidation classifies deterministic request-validation failures.
var ErrValidation = errors.New("validation failed")

// ErrNotFound is returned when a requested entity does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrNotInitialized is returned when the database has not been initialized
// (e.g., issue_prefix config is missing).
var ErrNotInitialized = errors.New("database not initialized")

// ErrUnsupported reports something the caller asked for that this backend
// cannot serve — a capability accessor it does not implement, or a request
// field it will not honor. It is a TYPE rather than a sentinel because the
// two facts a caller needs are which operation refused and which backend
// refused it, and neither survives a formatted string.
//
// A backend returns it instead of quietly doing something narrower. The case
// that made that rule explicit is Reader's Offset: the store-backed body
// rendered LIMIT without OFFSET, so a caller that paged with it received the
// first page over and over with no error to notice.
//
// It lives here so a caller holding only a role interface can classify the
// refusal with errors.As without importing internal/storage — or, since the
// capability shell is not an issue concept, without importing issueops either.
type ErrUnsupported struct {
	Op      string // method name, e.g. "AddLabel" or "Transaction.CreateIssues"
	Backend string // e.g. "dolt-server"
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("operation %q not supported by the %s backend", e.Op, e.Backend)
}

// THE AUTHORITY VOCABULARY. The bead graph is single-authority: one workspace
// holds a Scope, asserts a store-owned witness inside every transaction, and
// refuses everything else (engdocs/BDP_GRAPH_ARCHITECTURE.md §2b, A1/A7/A9).
// The refusals below are what that assertion says when it fails, and they are
// declared here rather than in graphops because none of them names a graph
// concept: "you are not the authority", "the state you recorded is gone",
// "the state moved under you", "the remote moved, sync first", "committed but
// not published", "too big", "not served yet" are things any plane with a
// witness, a publication step, or a size bound would say. The four refusals
// that DO name the plane — no Scope, a Scope exists, a Scope URL reused, a
// path in a gone state — stay in graphops, by the test in this file's doc.
//
// Nothing here says HOW a caller recovers; the graph verbs do. A sentinel is
// the classification, and the message is for a human reading a log.

// ErrNotAuthority reports that this workspace is not the authority for the
// thing it was asked to serve or mutate: no witness, a witness bound to another
// installation, a stale (authority_id, epoch), a lease another holder owns, or
// a substrate that cannot hold authority at all (an embedded or registered
// backend workspace in v0). Replication, restore and copy confer nothing; the
// remedies are explicit and operator-driven.
var ErrNotAuthority = errors.New("not the authority for this Scope")

// ErrStateRewound reports that the witness names a ledger head the store no
// longer contains — the shape a restore to an older state leaves behind. It is
// distinct from ErrNotAuthority because the remedy is different: the workspace
// IS the authority and must show continuity (a ledger snapshot) or rotate.
var ErrStateRewound = errors.New("state rewound: the recorded ledger head is no longer in the store")

// ErrStateChanged reports that the graph-state version observed inside a
// transaction differs from the one the witness recorded. The body that sees it
// stops without validating in its held transaction; the accessor validates the
// delta on its own, advances the witness, and retries once. A caller that sees
// it after that retry is looking at a refused delta.
var ErrStateChanged = errors.New("state changed under the recorded version")

// ErrSyncRequired reports a publication whose remote moved on another plane
// only (issue-plane divergence with no graph delta): the local commit is kept,
// nothing is undone, and the caller pulls before retrying. Hazard R vocabulary,
// deferred under A9 but part of the closed set so a client can classify it.
var ErrSyncRequired = errors.New("sync required: the remote moved outside the graph")

// ErrUnpublished reports a mutation that committed locally but whose
// publication failed for a reason other than a race: the commit stands, the
// witness carries the unpublished marker, and the next attempt retries the
// publication. Hazard R vocabulary, deferred under A9.
var ErrUnpublished = errors.New("committed locally but not yet published")

// ErrRepresentationTooLarge reports a value — a properties document, a
// descriptor, a request body — larger than the bound the serving surface
// advertises. The bound itself belongs to the surface (the store's value limit,
// the handler's body limit), not to this sentinel.
var ErrRepresentationTooLarge = errors.New("representation too large")

// ErrNotServedYet reports a surface that exists in the contract but is not yet
// served on this route — a collection read on the client route before the
// cursor ADR, a Scope that has not been minted and is being asked for through a
// door that never mints. It is the capability-level "not now" that
// ErrUnsupported's "not by this backend" is not.
var ErrNotServedYet = errors.New("not served yet")
