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
