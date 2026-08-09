package backend

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ErrUnsupported reports an operation unavailable on a storage backend. A
// backend that legitimately does not support a method returns a
// *ErrUnsupported naming that method; the conformance suite's
// RunUnsupportedContract asserts exactly that shape.
type ErrUnsupported = storage.ErrUnsupported

// Sentinel errors the storage contract requires from an implementation.
// Each is the SAME value as the internal sentinel (and, where noted in
// internal/storage, as the public issueops sentinel), so errors.Is matches
// across the module boundary. The conformance suite asserts these exact
// identities: a backend must return (or wrap) these values, not
// equivalently-worded errors of its own.
var (
	ErrAlreadyClaimed      = storage.ErrAlreadyClaimed
	ErrAlreadyExists       = storage.ErrAlreadyExists
	ErrAssigneeMismatch    = storage.ErrAssigneeMismatch
	ErrCloseBlocked        = storage.ErrCloseBlocked
	ErrCloseOpenChildren   = storage.ErrCloseOpenChildren
	ErrCommitIndeterminate = storage.ErrCommitIndeterminate
	ErrNotClaimable        = storage.ErrNotClaimable
	ErrNotFound            = storage.ErrNotFound
	ErrNotInitialized      = storage.ErrNotInitialized
	ErrNotOwner            = storage.ErrNotOwner
	ErrPrefixMismatch      = storage.ErrPrefixMismatch
	ErrStatusMismatch      = storage.ErrStatusMismatch
	ErrValidation          = storage.ErrValidation
	ErrVersionMismatch     = storage.ErrVersionMismatch

	// ErrFieldTooLong pairs with MaxFieldLen.
	ErrFieldTooLong = types.ErrFieldTooLong
)

// ClaimedByFragment and NotClaimableStatusFragment are the exact message
// fragments the claim path appends after ErrAlreadyClaimed / ErrNotClaimable
// to carry the conflicting assignee or status. A backend that produces claim
// conflicts itself must use the same format so ParseClaimConflict-style
// consumers keep working; see the documentation in internal/storage.
const (
	ClaimedByFragment          = storage.ClaimedByFragment
	NotClaimableStatusFragment = storage.NotClaimableStatusFragment
)
