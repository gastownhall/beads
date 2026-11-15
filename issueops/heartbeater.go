package issueops

import (
	"context"
	"fmt"
	"time"
)

// ErrLeaseNotHeld is the refusal a heartbeat answers when the caller holds no
// live claim on the issue: it is open, closed, released, reclaimed, moved off
// in_progress, or a wisp (wisps are never leased).
//
// IT ALSO MATCHES ErrNotClaimable, which is what the raw heartbeat has always
// answered for the same states, so a caller written against that sentinel
// keeps working when it moves onto this role.
var ErrLeaseNotHeld = fmt.Errorf("lease not held: %w", ErrNotClaimable)

// LeaseConflictError reports the state that refused a heartbeat, read inside
// the same transaction that found no lease row for the caller. It wraps the
// refusal rather than replacing it, the way ClaimConflictError does, so the
// sentinel still matches and a front door can classify the conflict from
// typed fields instead of parsing prose.
type LeaseConflictError struct {
	// IssueID names the issue the heartbeat was refused on.
	IssueID string
	// Holder is the foreign assignee that holds the claim. It is empty when
	// the refusal is about the status (no live claim) rather than a foreign
	// holder.
	Holder string
	// Status is the status the refusing transaction observed.
	Status Status
	// Ephemeral is true when the id names a wisp, which is never leased.
	// Status is then the wisp's own status, which may well be in_progress.
	Ephemeral bool
	// Err is the wrapped refusal. It matches ErrAlreadyClaimed (a foreign
	// holder) or ErrLeaseNotHeld (no live claim by the caller).
	Err error
}

func (e *LeaseConflictError) Error() string { return e.Err.Error() }

// Unwrap makes LeaseConflictError match the refusal it carries.
func (e *LeaseConflictError) Unwrap() error { return e.Err }

// The HeartbeatRequest fields a HeartbeatFieldError can name.
const (
	HeartbeatFieldActor   = "actor"
	HeartbeatFieldIssueID = "issue_id"
)

// HeartbeatFieldError reports a HeartbeatRequest refusal and the one field it
// is about, so a front door can name that field without parsing the message.
// It wraps ErrValidation.
type HeartbeatFieldError struct {
	// Field is one of the HeartbeatField* constants.
	Field string
	// Detail is the sentence the error reads as.
	Detail string
}

func (e *HeartbeatFieldError) Error() string { return e.Detail }

// Unwrap makes HeartbeatFieldError match ErrValidation.
func (e *HeartbeatFieldError) Unwrap() error { return ErrValidation }

// HeartbeatRequest refreshes the lease on one issue the actor holds — the
// shape behind `bd heartbeat`.
//
// Implementations never mutate caller-owned request values.
type HeartbeatRequest struct {
	// Actor is the lease holder refreshing its lease, and the fence's
	// subject: only the current holder may heartbeat. It is REQUIRED — a
	// blank Actor is ErrValidation — and so is one longer than
	// types.MaxFieldLen characters, which no lease row can hold.
	//
	// THE ROLE DOES NOT TRIM OR CASE-FOLD IT, Releaser.Actor's rule: the
	// value is compared with the lease row's holder as sent. A front door may
	// normalize before it calls the role: bd serve trims the body's actor by
	// the claim's rule (so a served claim and a served heartbeat agree on the
	// spelling), and the CLI passes its resolved actor through. A holder
	// spelled differently across layers (separator-insensitive, see
	// ReleaseRequest.ExpectedAssignee) still heartbeats: it misses the fast
	// path, and the slow path refreshes the lease row in place under this
	// spelling.
	Actor string
	// IssueID is the exact canonical id, on either plane, and must not be
	// empty. There is no fuzzy or prefix resolution: that is a front-door
	// convenience, for the reason GetRequest.ID gives.
	IssueID string
}

// HeartbeatResult reports one refreshed lease.
type HeartbeatResult struct {
	// IssueID is the issue whose lease was refreshed.
	IssueID string
	// Holder is the lease holder after the refresh: the request's Actor, in
	// the request's spelling.
	Holder string
	// HeartbeatAt is the server clock, in UTC and rounded to whole seconds
	// as the lease row's column rounds it: the stored value.
	HeartbeatAt time.Time
	// LeaseExpiresAt is HeartbeatAt plus the store's lease TTL: the deadline
	// the next heartbeat must beat before a reclaim may revert the claim.
	LeaseExpiresAt time.Time
	// Rearmed is true only when the heartbeat CREATED the caller's lease row:
	// the caller held a live claim but no lease row the caller holds existed
	// (a claim hand-doled through a generic update), so the heartbeat granted
	// a fresh one instead of refreshing it. A re-arm is recorded as a
	// mutation and remints the issue's revision token (see Heartbeater);
	// re-read before a guarded write. A refresh of the caller's own row
	// is never a re-arm, including one that changed no value (two heartbeats
	// in one second) and one under a differently spelled holder.
	//
	// false does not prove that nothing reminted the token: a transport that
	// resends a heartbeat whose answer was lost (the HTTP client does) reports
	// the resend's answer, a refresh of the lease row a committed re-arm
	// granted. A guarded write composed on the old revision then fails its
	// revision check once and re-reads.
	Rearmed bool
}

// Heartbeater refreshes a held lease — the capability behind `bd heartbeat` —
// and, like every other capability here, a role with its own accessor.
//
// IT IS A DIFFERENT QUESTION FROM Claimer. A claim takes ownership and is
// idempotent for the holder WITHOUT extending the lease (a re-claim returns
// before the lease is touched); a heartbeat proves the holder is alive and
// does nothing else. Folding one into the other would make every keepalive a
// claim attempt, with a claim's refusals and a claim's history.
//
// A REFRESH WRITES ONLY THE LEASE ROW. A heartbeat touches the ephemeral
// leases table and nothing else: it does not remint row_lock (so the holder's
// own revision token survives it and its guarded writes keep working), it
// records no version (current_revision and issue_versions stay put), it does
// not stamp updated_at, it records no event and no events-journal entry, it
// makes no version-control commit, and it fires no hook. It is the hottest
// write in a fleet, and every one of those would make it expensive or noisy.
//
// A RE-ARM IS RECORDED AS A MUTATION; A REFRESH IS NOT. A re-arm (Rearmed:
// the caller held a live claim with no lease row, a claim hand-doled through
// a generic update) is recorded the way every bead mutator records a write:
// it remints row_lock, stamps updated_at, records a lease_rearmed event, an
// events-journal entry and a version, makes a version-control commit, and
// fires on_update. The remint is what makes the grant collide with a release
// or close racing it, instead of committing beside it and leaving a lease on
// an issue that is no longer claimed. Re-arms are rare (a hand-doled claim's
// first heartbeat), so the cost lands once per such claim.
//
// THE LIVE-CLAIM FENCE IS AN INVARIANT AND A PREDICATE. Every path that ends
// or transfers a claim deletes the lease row, and a re-arm racing such a path
// collides with it on row_lock, so a lease row exists only while its claim is
// live. The refresh also matches the lease row by holder AND requires, in the
// same transaction, that the issue is in_progress with that holder as assignee,
// so a lease row orphaned any other way fails the refresh, and the refusal
// deletes it. A refresh that lands is a refresh of a live claim.
//
// EVERY LEG RUNS ONE BODY: dolt, embedded dolt and the unit-of-work provider
// wrap internal/storage/issueops.ExecuteHeartbeatInTx.
//
// Deterministic request-validation failures match ErrValidation. Result
// values are unspecified when error is non-nil. Every refusal below leaves
// persistent state unchanged, with one exception: a *LeaseConflictError
// refusal deletes the caller's own lease row if one survives on an issue the
// caller no longer holds a live claim on (an orphan; never a live holder's
// lease).
type Heartbeater interface {
	// Heartbeat refreshes the lease on request.IssueID held by request.Actor.
	//
	// REFUSALS:
	//
	//   - a blank Actor, one longer than types.MaxFieldLen characters, or an
	//     empty IssueID: a *HeartbeatFieldError (ErrValidation), before
	//     anything is read;
	//   - an IssueID that names no row on either plane: ErrNotFound;
	//   - an issue another actor holds: a *LeaseConflictError naming the
	//     Holder, wrapping ErrAlreadyClaimed;
	//   - a wisp, or an issue the actor holds no live claim on (open, closed,
	//     released, reclaimed, or any status but in_progress): a
	//     *LeaseConflictError wrapping ErrLeaseNotHeld, which also matches
	//     ErrNotClaimable; Ephemeral is set for a wisp.
	Heartbeat(ctx context.Context, request HeartbeatRequest) (HeartbeatResult, error)
}
