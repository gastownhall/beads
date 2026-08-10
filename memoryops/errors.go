package memoryops

import "github.com/steveyegge/beads/beadserrors"

// ErrValidation classifies this role's deterministic request-validation
// failures. It is an ALIAS of beadserrors.ErrValidation, not a second sentinel,
// and the identity is the point: the HTTP problem classifier, cmd/bd's error
// handling and every conformance suite already errors.Is against that one
// value, so a memoryops-flavored twin would make each of them double-match
// forever — one vocabulary with two doorplates instead of two vocabularies.
//
// It is re-exported here rather than left for callers to reach through
// beadserrors so that code holding only the Memories interface can classify a
// refusal without knowing a second package exists, which is the courtesy
// issueops.ErrUnsupported's doc extends for the same reason.
//
// The alias points at beadserrors rather than at issueops, where this value
// used to be declared. Reaching through the issue package said the memory plane
// is downstream of the issue plane — it is not, they are siblings over one
// config table — and the import dragged internal/types along to say it. This
// leaf's whole dependency set is now beadserrors and context.
var ErrValidation = beadserrors.ErrValidation

// THERE IS DELIBERATELY NO ErrNotFound ON THIS ROLE.
//
// beadserrors declares one; this leaf does not re-export it. The storage seam
// beneath it cannot tell an absent config row from a row stored as the empty
// string (issueops/workspaceconfig.go states the same conflation for settings,
// and it is the same table). A role that answered a Recall of an unknown key
// with ErrNotFound would be minting an error out of a distinction it cannot
// actually see, and the first out-of-band empty write would make it a lie.
//
// Misses are RESULT-CARRIED instead — RecallResult.Found, ForgetResult.Found —
// and the front doors translate: the CLI to its SilentExit contract, an HTTP
// door to its 404. That keeps the invention where a door can justify it.
