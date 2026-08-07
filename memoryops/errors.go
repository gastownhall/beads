package memoryops

import "github.com/steveyegge/beads/issueops"

// ErrValidation classifies this role's deterministic request-validation
// failures. It is an ALIAS of issueops.ErrValidation, not a second sentinel,
// and the identity is the point: the HTTP problem classifier, cmd/bd's error
// handling and every conformance suite already errors.Is against that one
// value, so a memoryops-flavored twin would make each of them double-match
// forever — one vocabulary with two doorplates instead of two vocabularies.
//
// It is re-exported here rather than left for callers to reach through
// issueops so that code holding only the Memories interface can classify a
// refusal without knowing the issue package exists, which is the courtesy
// issueops.ErrUnsupported's doc extends for the same reason.
var ErrValidation = issueops.ErrValidation

// THERE IS DELIBERATELY NO ErrNotFound ON THIS ROLE.
//
// The storage seam beneath it cannot tell an absent config row from a row
// stored as the empty string (issueops/workspaceconfig.go:41-52 states the same
// conflation for settings, and it is the same table). A role that answered a
// Recall of an unknown key with ErrNotFound would be minting an error out of a
// distinction it cannot actually see, and the first out-of-band empty write
// would make it a lie.
//
// Misses are RESULT-CARRIED instead — RecallResult.Found, ForgetResult.Found —
// and the front doors translate: the CLI to its SilentExit contract, an HTTP
// door to its 404. That keeps the invention where a door can justify it.
