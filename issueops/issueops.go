// Package issueops declares the public values used for guarded issue mutations.
package issueops

import (
	"context"
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Issue is the canonical Beads issue model.
type Issue = types.Issue

// Status is an issue lifecycle status.
type Status = types.Status

// IssueType classifies an issue.
type IssueType = types.IssueType

// PersistenceMode selects the complete persistence state for an issue.
type PersistenceMode = types.PersistenceMode

// Status values accepted by issue operations.
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusBlocked    = types.StatusBlocked
	StatusDeferred   = types.StatusDeferred
	StatusClosed     = types.StatusClosed
	StatusPinned     = types.StatusPinned
	StatusHooked     = types.StatusHooked
)

// Persistence mode values accepted by issue operations.
const (
	PersistenceModePersistent = types.PersistenceModePersistent
	PersistenceModeEphemeral  = types.PersistenceModeEphemeral
	PersistenceModeNoHistory  = types.PersistenceModeNoHistory
)

// Waits-for gate values accepted by issue operations.
const (
	WaitsForAllChildren = types.WaitsForAllChildren
	WaitsForAnyChildren = types.WaitsForAnyChildren
)

// Field distinguishes an omitted value from an explicitly supplied zero value.
type Field[T any] struct {
	// Set reports whether Value was supplied.
	Set bool
	// Value is the supplied value, including a zero or nil value.
	Value T
}

// LabelPatch describes ordered label replacement and incremental edits.
// Operations apply Replace, then Add, then Remove, so removal wins when the
// same label appears in more than one edit.
//
// The VALUES an edit carries answer to the same field rules a create's labels
// do: a label longer than the column is refused with ErrFieldTooLong and the
// whole update writes nothing. Repetition is free in both directions — the
// same label named twice in one edit is applied once, and removing a label the
// issue does not carry is a no-op that reports Changed false.
//
// An EMPTY-STRING entry is DROPPED — not written, and not refused. A label
// row carrying "" renders as nothing and matches nothing, so writing one only
// stores junk; refusing the whole update would let one stray entry fail an
// otherwise-good multi-label edit. Dropping is a no-op rather than a partial
// write: an Add carrying only "" changes nothing and reports Changed false.
// An empty entry already stored is left alone by Add and Remove and cleared by
// a Replace that omits it, which is the ordinary set behavior and not a
// migration.
type LabelPatch struct {
	// Add contains labels to add after any replacement.
	Add []string
	// Remove contains labels to remove after replacement and addition.
	Remove []string
	// Replace supplies the starting complete label set when Set is true.
	Replace Field[[]string]
}

// MetadataPatch describes metadata replacement or ordered incremental edits.
// Replace is mutually exclusive with Merge, Set, and Unset. Without Replace,
// operations apply Merge, then Set keys in deterministic order, then Unset, so
// unsetting a key wins over setting or merging that key.
type MetadataPatch struct {
	// Replace replaces the complete metadata document when Set is true. A nil or
	// empty Value clears metadata; a nonempty Value must be valid JSON.
	//
	// CLEARING STORES THE EMPTY JSON DOCUMENT. An issue's metadata is never
	// SQL NULL, and it is never NULL at creation either, so "created with no
	// metadata" and "given metadata and then cleared" are the SAME stored
	// value rather than two states for one user-visible fact. A consumer
	// filtering on `metadata IS NULL` therefore matches neither, and one
	// asking for an empty document matches both.
	//
	// Read back through a result or the Reader role, that same empty document
	// arrives as an EMPTY json.RawMessage rather than the two bytes of an
	// empty object — so a caller must treat nil, empty and {} as one value on
	// the way out, exactly as this field treats them on the way in.
	Replace Field[json.RawMessage]
	// Merge merges a metadata document into the current value. A nil or empty
	// Value is invalid; a nonempty Value must be a JSON object.
	Merge Field[json.RawMessage]
	// Set writes individual metadata keys in deterministic key order.
	Set map[string]json.RawMessage
	// Unset removes individual metadata keys after all other edits.
	Unset []string
}

// IssuePatch describes fields that can change on an issue.
type IssuePatch struct {
	Title              Field[string]
	Description        Field[string]
	Design             Field[string]
	AcceptanceCriteria Field[string]
	// Notes replaces the notes and is mutually exclusive with AppendNotes.
	Notes Field[string]
	// AppendNotes appends to the notes and is mutually exclusive with Notes.
	AppendNotes Field[string]
	SpecID      Field[string]
	AwaitID     Field[string]
	// Status sets the issue's status. A status that crosses from outside the
	// configured done category into it answers to close policy: the update
	// refuses with CloseOpenChildrenError or ErrCloseBlocked unless
	// UpdateRequest.ForceClosePolicy is set. A done-to-done change and a move
	// out of the done category are unaffected. Close and Reopen remain the
	// operations that carry the full lifecycle policy — a crossing update
	// applies the close policy, not the close semantics.
	Status           Field[Status]
	Priority         Field[int]
	IssueType        Field[IssueType]
	Assignee         Field[string]
	Owner            Field[string]
	ClosedBySession  Field[string]
	EstimatedMinutes Field[*int]
	ExternalRef      Field[*string]
	DueAt            Field[*time.Time]
	DeferUntil       Field[*time.Time]
	// Persistence selects the complete persistence state. It is unchanged when
	// unset; a set value must be a known PersistenceMode. A same current mode is
	// a representation-preserving no-op. Every aggregate move is atomic.
	// Persistent preserves an existing durable unversioned class; promotion
	// from a wisp selects normalized versioned storage. Ephemeral and NoHistory
	// select wisp retention modes, never durable unversioned storage. Command
	// adapters translate their own flag spelling before constructing this
	// request.
	Persistence Field[PersistenceMode]
	// ParentID is unchanged when unset. A set empty value removes all outgoing
	// parent-child edges. A set nonempty value atomically replaces all parents
	// with exactly that target and does not inherit labels.
	ParentID Field[string]
	Labels   LabelPatch
	Metadata MetadataPatch
}

// CreateDependency describes a dependency created with an issue.
type CreateDependency struct {
	TargetID string
	Type     types.DependencyType
	// Reverse writes the dependency from TargetID to the new issue.
	Reverse  bool
	Metadata string
	// ThreadID associates this dependency with a discussion thread.
	ThreadID string
}

// WaitsFor describes the typed metadata for a DepWaitsFor dependency. It
// records a readiness primitive; it does not define scheduling or execution
// policy.
type WaitsFor struct {
	// SpawnerID is required and identifies the dependency target whose children
	// are observed.
	SpawnerID string
	// Gate selects the readiness condition. Empty defaults to
	// WaitsForAllChildren; otherwise only the exported waits-for gate constants
	// are valid.
	Gate string
}

// CreateRequest describes one atomic issue creation.
type CreateRequest struct {
	Actor string
	// Issue is included in the request's single deep snapshot at method entry;
	// every transaction attempt clones that same entry snapshot. Create accepts
	// its ID, content, workflow, assignment, timestamps, scheduling,
	// external-reference, metadata, wisp, storage-class, marker, gate, source,
	// molecule, work-type, and event fields using the canonical create rules. It
	// ignores ContentHash, RowVersion, lease state, compaction state, routing
	// overrides, hydration flags, and derived fields. Labels are authoritative.
	// Issue.Comments and Issue.Dependencies must be empty; supply edges through
	// the request's own Dependencies field.
	Issue *Issue
	// ParentID creates a typed DepParentChild edge. It must not duplicate an
	// explicit edge in Dependencies.
	ParentID string
	// InheritLabelsFromParent copies the parent's labels at creation.
	InheritLabelsFromParent bool
	// Dependencies is the authoritative set of explicit edges created with the
	// issue. Duplicate source-target pairs are rejected. A different type for an
	// existing pair is rejected with DependencyTypeConflictError rather than
	// silently retyping the edge.
	Dependencies []CreateDependency
	// WaitsFor creates a typed DepWaitsFor edge. It must not duplicate an
	// explicit edge in Dependencies.
	WaitsFor *WaitsFor
	// ForceIDPrefix permits an explicit ID outside the configured prefix.
	ForceIDPrefix bool
	// IDPrefix is the prefix an explicit Issue.ID is checked against, and it
	// OVERRIDES the one the substrate has configured. Empty means "use the
	// substrate's", which is the ordinary case.
	//
	// It exists because the workspace's own config.yaml `issue-prefix` wins
	// over the database's (GH#4957) and no implementation can see config.yaml
	// — a shared server's database knows only its own prefix. Resolving that
	// is the front door's job, the same way DeleteRequest.IDs are exact
	// because resolution belongs to the front door; the role enforces what it
	// is handed. Without it the two `bd create` routes disagree about which
	// ids a workspace may mint.
	IDPrefix string
}

// UpdateRequest describes an issue update.
type UpdateRequest struct {
	Actor, IssueID string
	Patch          IssuePatch
	// Claim atomically claims the issue for Actor. It first sets Assignee to
	// Actor and Status to StatusInProgress, then applies Patch; a set
	// Patch.Assignee or Patch.Status overrides the corresponding claim value.
	// An issue is eligible when its status is built-in StatusOpen or a configured
	// active status and it is unassigned, assigned to Actor, or assigned to a
	// configured pool. StatusInProgress assigned to the same Actor is an
	// idempotent success. A foreign assignment returns ErrAlreadyClaimed; an
	// ineligible status returns ErrNotClaimable.
	// ExpectedAssignee and ExpectedStatus must be nil when Claim is true because
	// the claim has its own compare-and-set eligibility rules.
	Claim bool
	// ForceAssigneeTransfer bypasses only a genuine transfer away from a foreign
	// live in-progress owner. Reasserting the exact current foreign assignee is
	// idempotent and needs no force. Assignees configured as claim.pools aliases
	// are transferable without force. The zero value enforces the fence. It has
	// no effect with Claim or an omitted Patch.Assignee and must be false with
	// ExpectedAssignee. It never bypasses other guards — in particular it is
	// independent of ForceClosePolicy, and a request that sets it without
	// Patch.Assignee is invalid. A command adapter with a single --force flag
	// therefore maps that flag to both fields, conditioning this one on an
	// assignee edit.
	ForceAssigneeTransfer bool
	// ForceClosePolicy bypasses only close policy — the open-children refusal
	// and the live-direct-blocker refusal — for a Patch.Status that crosses into
	// the done category. The zero value enforces the policy. It has no effect
	// without such a status change, and never bypasses validation,
	// ExpectedVersion, ExpectedAssignee, ExpectedStatus, or the assignee fence.
	// It is the update-side spelling of CloseRequest.Force; a command adapter
	// that maps one flag to both spells both.
	ForceClosePolicy bool
	// ExpectedVersion requires the current row version to match before the claim
	// and patch. It is an independent precondition and may be combined with Claim.
	ExpectedVersion *int64
	// ExpectedAssignee requires the current assignee to match. A match authorizes
	// the requested Patch.Assignee transfer: this compare-and-set replaces the
	// ordinary anti-steal fence. It must be nil with Claim, and
	// ForceAssigneeTransfer must be false when it is non-nil.
	ExpectedAssignee *string
	// ExpectedStatus requires the current status to match. It must be nil when
	// Claim is true.
	ExpectedStatus *Status
	// IssuePlaneOnly restricts the update to the issue plane: an ID that names
	// a wisp is ErrNotFound rather than an ephemeral row to update. The zero
	// value keeps the both-plane auto-resolve every caller gets today.
	//
	// Resolution happens INSIDE the transaction the update runs in, so a
	// caller cannot be handed the plane an earlier read saw. It exists for the
	// surface that serves durable issues only — refusing there rather than
	// carrying a private copy of the resolve is what keeps the two front doors
	// on one implementation.
	IssuePlaneOnly bool
	// Provenance labels the version-control history entry this mutation
	// records, naming the surface the update came from. Empty selects the
	// implementation's default label.
	//
	// It NEVER changes WHETHER history is recorded — only how the entry reads.
	// An update that records none records none with it set, one that records
	// one records one with it empty, and an implementation with no history to
	// label ignores it. Persistence, not this, is what decides whether an issue
	// is versioned at all.
	Provenance string
}

// CloseRequest describes an issue closure.
type CloseRequest struct {
	Actor   string
	IssueID string
	// Reason records why the issue was closed. It is stored on the issue and
	// read back on CloseResult.Issue.CloseReason, alongside the ClosedAt the
	// same close stamps.
	//
	// THE FIRST CLOSE WINS. A second Close of an already-closed issue is the
	// no-op CloseResult.Changed describes, so it writes neither this nor
	// Session and the stored pair keeps the values the first close gave them
	// — a replayed close cannot rewrite the record of why the work ended. A
	// Reopen clears both, because they describe a closure that no longer
	// holds.
	Reason string
	// Session names the working session that closed the issue. It is stored
	// and read back on CloseResult.Issue.ClosedBySession, under the same
	// first-close-wins rule as Reason.
	Session string
	// Force bypasses only blocker and open-child close policy. It never bypasses
	// validation, ExpectedVersion, or lifecycle rules.
	Force bool
	// ExpectedVersion requires the current row version to match and is checked
	// before an idempotent close.
	ExpectedVersion *int64
}

// ReopenRequest describes an issue reopening.
type ReopenRequest struct {
	Actor   string
	IssueID string
	// Reason records why literal closed and configured done statuses move to
	// open. Non-done statuses are unchanged.
	//
	// WHERE it is recorded is the issue's EVENT HISTORY, carried on the
	// reopened entry the move itself records. It is not stored on a field of
	// the issue and ReopenResult does not surface it, so a caller that wants
	// it back reads the issue's events — the same place the Actor attribution
	// for the reopen lives. A reopen with no reason still records that entry;
	// it simply carries none.
	Reason string
	// ExpectedVersion requires the current row version to match and is checked
	// before a non-done no-op.
	ExpectedVersion *int64
	// Provenance labels the version-control history entry this reopen records,
	// naming the surface it came from. Empty selects the implementation's
	// default label, and the implementations do not agree on what that is — the
	// store-backed ones write "bd: reopen issue" and the unit-of-work one
	// "reopen issue", neither naming the issue. A caller that wants one entry
	// no matter which backend served it spells one, exactly as
	// UpdateRequest.Provenance describes.
	//
	// It NEVER changes WHETHER history is recorded — only how the entry reads.
	Provenance string
}

// CreateResult reports the created issue as a detached snapshot with labels,
// dependencies, and persisted comments.
type CreateResult struct {
	// Issue is a detached snapshot with labels, dependencies, and persisted
	// comments.
	Issue *Issue
}

// UpdateResult reports the post-update issue as a detached post-state snapshot
// with labels and dependency records. Comments are omitted.
type UpdateResult struct {
	// Issue is a detached post-state snapshot with labels and dependency records.
	// Comments are omitted.
	Issue *Issue
	// Changed reports whether the request persisted a semantic mutation. It is
	// false for same-value patches and no-op updates.
	Changed bool
}

// CloseResult reports the post-close issue as a detached post-state snapshot
// with labels and dependency records. Comments are omitted.
type CloseResult struct {
	// Issue is a detached post-state snapshot with labels and dependency records.
	// Comments are omitted.
	Issue *Issue
	// Changed reports whether closing persisted a semantic mutation. It is false
	// for an idempotent re-close.
	Changed bool
	// OpenChildren is the number of open children observed by a forced close.
	// It is reported even for an idempotent re-close.
	OpenChildren int
}

// ReopenResult reports the post-reopen issue as a detached post-state snapshot
// with labels and dependency records. Comments are omitted.
type ReopenResult struct {
	// Issue is a detached post-state snapshot with labels and dependency records.
	// Comments are omitted.
	Issue *Issue
	// Changed reports whether reopening persisted a semantic mutation. It is
	// false when non-done statuses are unchanged.
	Changed bool
}

// Lifecycle describes guarded issue mutations. A new capability gets a new
// role interface and its own accessor; never append a method here.
// Deterministic request validation failures match ErrValidation; when a
// more-specific validation sentinel applies, it remains matchable too.
// Implementations never mutate
// caller-owned request values. Callers must not concurrently mutate
// request-owned mutable values until the call returns; snapshotting does not
// make Go data races safe. Implementations snapshot all request inputs once at
// method entry, deep-cloning caller-owned mutable values into that snapshot.
// They deep-clone that snapshot before every transaction attempt, including
// retries, and apply validation, defaulting, and normalization only to
// attempt-local clones. Result values are unspecified when error is non-nil.
// Refusals and deterministic validation failures leave persistent state
// unchanged. Other operational or commit-finalization errors can have an
// indeterminate durable outcome; callers must reread state before retrying.
type Lifecycle interface {
	// Create validates and commits the complete request as one atomic mutation.
	// It is create-only across issues and wisps: an occupied ID returns
	// ErrAlreadyExists and leaves persistent state unchanged. A refusal or
	// validation error also leaves no partial persistent state.
	Create(context.Context, CreateRequest) (CreateResult, error)
	// Update validates guards and commits the complete request as one atomic
	// mutation. A Patch.Status that crosses from outside the configured done
	// category into it answers to close policy: an unforced crossing with open
	// children returns CloseOpenChildrenError and one with a live direct blocker
	// returns ErrCloseBlocked, both without mutation. ForceClosePolicy bypasses
	// those two refusals and nothing else. A Claim that loses its
	// compare-and-set returns *ClaimConflictError carrying the state that beat
	// it. A refusal or validation error leaves persistent state unchanged.
	Update(context.Context, UpdateRequest) (UpdateResult, error)
	// Close validates guards and commits the complete request as one atomic
	// mutation. It moves the issue to literal StatusClosed, including from a
	// configured done status. ExpectedVersion is checked first, including for an
	// idempotent close. An unforced close with open children returns
	// CloseOpenChildrenError without mutation. Force bypasses blocker and
	// open-child policy and reports OpenChildren, including for an idempotent
	// re-close. A refusal or validation error leaves persistent state unchanged.
	Close(context.Context, CloseRequest) (CloseResult, error)
	// Reopen validates guards and commits the complete request as one atomic
	// mutation. It moves literal StatusClosed and configured done statuses to
	// StatusOpen; non-done statuses unchanged. A refusal or validation error
	// leaves persistent state unchanged.
	Reopen(context.Context, ReopenRequest) (ReopenResult, error)
}

// ErrFieldTooLong preserves the canonical model sentinel identity for a value
// that exceeds its field's maximum length. The rest of the operation error
// vocabulary is declared in errors.go.
var ErrFieldTooLong = types.ErrFieldTooLong
