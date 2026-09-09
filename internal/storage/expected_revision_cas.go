package storage

import "time"

// The DATABASE-FREE boundary types for R16's whole-of-state per-record CAS
// write (gastownhall/beads#5898 revision 9, this slice: be-x5jqd.3 / #6133).
//
// THEY LIVE HERE, RATHER THAN IN internal/storage/issueops where the shared
// transaction body (CompareAndSetVersionInTx) lives, for the same dependency
// direction CompareAndSetKeyPlan is here for: the unit-of-work leg reaches
// that body through the domain issue repository, so internal/storage/domain
// has to name these types in its own interface — and internal/storage/domain
// cannot import internal/storage/issueops (the two already import each
// other's sibling packages: internal/storage/domain/db imports
// internal/storage/issueops, and internal/storage/issueops imports
// internal/storage/domain). This package is the one both sides can already
// reach: internal/storage/domain imports it today for CompareAndSetKeyPlan,
// and internal/storage/issueops imports it today for the same type.
//
// This is independent of CompareAndSetKeyPlan/CompareAndSetKeyResult
// (metadata_cas.go) — a different role over a different table — and of
// issue_versions' versioned-history plumbing (#6135/#6358/#6379): neither
// shares a type with this file.

// ExpectedRevisionAttribution is who/what/why/when produced a version of a
// per-record CAS record. Actor, Agent and Message are independently
// nullable; a zero At means "now", filled in by the implementation rather
// than required of the caller.
type ExpectedRevisionAttribution struct {
	Actor   *string
	Agent   *string
	Message *string
	At      time.Time
}

// CompareAndSetVersionPlan is one whole-of-state per-record CAS write
// request (R16).
type CompareAndSetVersionPlan struct {
	// ID is the record id. Must not be empty (R17-d3).
	ID string
	// Expected is the Address the record's current version must have for the
	// write to be accepted, or nil for an unconditional write (R16-b) —
	// which, against an id with no current version, CREATES it: the only way
	// a record ever gets its first version.
	Expected *string
	// Patch is merged into the record's current durable state (whole-of-state
	// merge, R16-c): every key Patch names is set on the record; every key
	// already present and not named survives untouched.
	Patch map[string]any
	// Attribution attributes the write, if accepted.
	Attribution ExpectedRevisionAttribution
}

// CompareAndSetVersionResult is the outcome of one CompareAndSetVersionPlan.
// Accepted and Refusal are mutually exclusive: read Refusal only when
// Accepted is false, read NewVersion only when it is true.
type CompareAndSetVersionResult struct {
	Accepted   bool
	NewVersion string
	Refusal    *ExpectedRevisionRefusal
}

// ExpectedRevisionRefusal is R17's typed outcome for a write that named a
// version no longer current — never a generic error, never indistinguishable
// from an accepted write (R17-c/d1).
type ExpectedRevisionRefusal struct {
	// RefusingVersion is the Address that was actually current when the
	// write was evaluated (R17-a).
	RefusingVersion string
	// Attribution is RefusingVersion's ChangeAttribution, as persisted by the
	// write that produced it (R17-b).
	Attribution ExpectedRevisionAttribution
}
