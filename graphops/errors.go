package graphops

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/beadserrors"
)

// The error vocabulary, in two halves.
//
// The first half is ALIASED from beadserrors: Go aliases, so every value is
// identical to the one declared there and one errors.Is arm matches it under
// either name. They are re-exported here for the same courtesy issueops and
// memoryops extend — code holding one graphops role can classify a refusal
// without discovering a second package.
//
// The second half is DECLARED here. engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md
// B2 lists all eleven graph refusals as "declared in beadserrors, aliased
// here", and beadserrors's own charter says a refusal that names a domain
// concept stays in the leaf that defines the concept. Four of the eleven name
// this plane — a Scope, a Scope URL, a path in an allocation state — and those
// four live here; the seven that any plane with a witness would need live in
// beadserrors, per its test.
//
// DECISION: the split above. The B2 sentence was read against the charter it
// cites, and the charter won for the four domain-naming refusals. The choice is
// reversible at no cost to callers: an alias preserves identity in both
// directions, so moving a declaration later leaves every errors.Is arm intact.

// ErrValidation classifies a deterministic request-validation failure: a path
// that is not canonical, a properties document that is not a JSON object, a
// Reference that claims the Scope without being a Bead's canonical spelling, a
// descriptor that breaks the closed shape. It is beadserrors.ErrValidation.
var ErrValidation = beadserrors.ErrValidation

// ErrNotFound reports a path, descriptor or Scope row that is not there — or
// not visible, since the Read profile answers an unknown identity and a hidden
// one identically. A path in a gone state is reported through GoneError, which
// matches this sentinel too; see its doc.
var ErrNotFound = beadserrors.ErrNotFound

// ErrUnsupported reports a capability this backend does not serve. A store
// without the bead graph answers every BeadGraph* accessor with it, Op set to
// the accessor's name. It is beadserrors.ErrUnsupported, re-exported.
type ErrUnsupported = beadserrors.ErrUnsupported

// ErrNotAuthority reports that this workspace is not the authority for the
// Scope: no witness, a witness bound to another installation, a stale
// (authority_id, epoch), a lease another holder owns, or a substrate that
// cannot hold authority in v0. See beadserrors.ErrNotAuthority.
var ErrNotAuthority = beadserrors.ErrNotAuthority

// ErrStateRewound reports a witness whose ledger head the store no longer
// contains — a restore to an older state. See beadserrors.ErrStateRewound.
var ErrStateRewound = beadserrors.ErrStateRewound

// ErrStateChanged reports a graph-state version that moved under the
// witness's recorded one. See beadserrors.ErrStateChanged.
var ErrStateChanged = beadserrors.ErrStateChanged

// ErrSyncRequired reports a kept local commit whose remote moved on another
// plane only. See beadserrors.ErrSyncRequired.
var ErrSyncRequired = beadserrors.ErrSyncRequired

// ErrUnpublished reports a kept local commit whose publication failed and
// will be retried. See beadserrors.ErrUnpublished.
var ErrUnpublished = beadserrors.ErrUnpublished

// ErrRepresentationTooLarge reports a value beyond the serving surface's
// advertised bound. See beadserrors.ErrRepresentationTooLarge.
var ErrRepresentationTooLarge = beadserrors.ErrRepresentationTooLarge

// ErrNotServedYet reports a contract surface not yet served on this route.
// See beadserrors.ErrNotServedYet.
var ErrNotServedYet = beadserrors.ErrNotServedYet

// ErrNoScope reports a workspace in which no Scope has been minted: the
// singleton Scope row is absent. IdentityReader.Read answers it, every
// protected read answers it, and ScopeBootstrapper.Mint is the only thing that
// clears it.
var ErrNoScope = errors.New("no Scope has been minted in this workspace")

// ErrScopeExists reports a Mint against a workspace whose Scope row already
// exists. Mint happens once; a second Scope in the same store is a different
// workspace.
var ErrScopeExists = errors.New("a Scope is already minted in this workspace")

// ErrURLReused reports a Scope URL that this store has refused for good: a URL
// rotated away, or one a restore could not show continuity for, is recorded in
// the Scope history by a refuse_url event and can never name a Scope here
// again (BDP: a canonical URL is never reassigned in the lifetime of the
// logical Scope).
var ErrURLReused = errors.New("Scope URL refused: it was rotated away and may not be reused")

// GoneError reports a canonical path that once named a Resource and no longer
// does: the allocation is pruned, erased, or reserved (ledger-applied with no
// row behind it). It carries the path and the allocation state so the BDP
// handler can serve the authorization-gated 410 disclosure (resource-pruned,
// resource-erased) to a caller entitled to it.
//
// It MATCHES ErrNotFound under errors.Is, deliberately. To every caller not
// authorized for the subject's retained history, BDP requires a gone path to
// be indistinguishable from an unknown one — the same 404 — and a caller that
// classifies with a plain errors.Is(err, ErrNotFound) arm gets exactly that
// default. A handler that may disclose more checks errors.As for this type
// FIRST. A reserved allocation is the same to a reader as a pruned one:
// nothing to serve, never reusable.
//
// DECISION: the Is(ErrNotFound) relation. The design lists GoneError beside
// ErrNotFound without stating one; the spec's "same 404 to every other caller"
// is the default this relation encodes, and a handler that wants the 410 has
// to opt in by asking for the type.
type GoneError struct {
	// Path is the canonical Scope-relative path that is gone.
	Path string
	// State is the allocation's state: AllocationReserved, AllocationPruned or
	// AllocationErased. Never AllocationLive — a live allocation is not gone.
	State AllocationState
}

func (e *GoneError) Error() string {
	return fmt.Sprintf("%s is gone (%s)", e.Path, e.State)
}

// Is makes a GoneError match ErrNotFound: the undisclosed reading of a gone
// path is "not found". See the type's doc.
func (e *GoneError) Is(target error) bool { return target == ErrNotFound }
