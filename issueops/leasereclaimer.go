package issueops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ReclaimFilter scopes which stale-lease issues a reclaim may revert. See
// types.ReclaimFilter: this is the same value, aliased into this package the
// way GetManyRequest's sibling roles alias their types.* shapes.
type ReclaimFilter = types.ReclaimFilter

// ReclaimedLease names one issue whose stale lease a reclaim reverted, plus
// the owner it took the lease from and the fresh revision the revert minted.
// See types.ReclaimedLease.
type ReclaimedLease = types.ReclaimedLease

// MaxReclaimIDs is the largest ReclaimFilter.IDs this role accepts in one
// call. It is the same number and the same reasoning as MaxGetManyIDs: a flat
// bound on the request rather than a cost model over the sweep it scopes, so
// one call cannot hold the revert transaction open over an unbounded id list.
const MaxReclaimIDs = 1000

// TooManyReclaimIDsError reports that a ReclaimRequest's Filter.IDs named more
// ids than this role accepts in one call. It wraps ErrValidation, matching
// TooManyIDsError's shape for the same reason: a caller that only checks for a
// usable request classifies it correctly without knowing this type exists.
type TooManyReclaimIDsError struct {
	// Requested is the number of ids Filter.IDs named, counted before
	// deduplication.
	Requested int
	// Cap is MaxReclaimIDs, carried on the error so a caller does not need the
	// constant in scope to report what it hit.
	Cap int
}

func (e *TooManyReclaimIDsError) Error() string {
	return fmt.Sprintf("reclaim accepts at most %d scoped ids, got %d", e.Cap, e.Requested)
}

// Unwrap makes TooManyReclaimIDsError match ErrValidation.
func (e *TooManyReclaimIDsError) Unwrap() error { return ErrValidation }

// The ReclaimRequest fields a ReclaimFieldError can name.
const (
	ReclaimFieldActor         = "actor"
	ReclaimFieldOlderThan     = "older_than"
	ReclaimFieldIDs           = "ids"
	ReclaimFieldAssignees     = "assignees"
	ReclaimFieldLabels        = "labels"
	ReclaimFieldLabelsAny     = "labels_any"
	ReclaimFieldExcludeLabels = "exclude_labels"
)

// ReclaimFieldError reports a ReclaimRequest refusal and the one field it is
// about, so a front door can name that field without parsing the message. It
// wraps ErrValidation. The id cap is the one refusal that is not one of these:
// it is a *TooManyReclaimIDsError, always about ReclaimFieldIDs.
type ReclaimFieldError struct {
	// Field is one of the ReclaimField* constants.
	Field string
	// Detail is the sentence the error reads as.
	Detail string
}

func (e *ReclaimFieldError) Error() string { return e.Detail }

// Unwrap makes ReclaimFieldError match ErrValidation.
func (e *ReclaimFieldError) Unwrap() error { return ErrValidation }

// ReclaimRequest describes one sweep of stale leases — the shape behind
// `bd reclaim`.
//
// Implementations never mutate caller-owned request values: Filter's slices
// are read, never written through.
type ReclaimRequest struct {
	// Actor attributes every reverted row's recovery event and its journaled
	// update. It is REQUIRED — an empty or all-blank Actor is ErrValidation —
	// for the same reason Releaser.Release requires one: a reclaim is the
	// moment work stops being owned, and the one question asked of a reverted
	// row's history afterwards is who ran the sweep that freed it.
	Actor string
	// OlderThan is the grace period past a lease's own TTL: only a lease whose
	// lease_expires_at is more than OlderThan in the past is eligible. It must
	// not be negative — a negative grace reaches into the future and reclaims
	// leases that have not yet expired by their own TTL, which is not a wider
	// sweep, it is a different operation this role does not perform.
	OlderThan time.Duration
	// Filter narrows which stale-lease issues are eligible; the zero value
	// reclaims every stale lease workspace-wide. See types.ReclaimFilter for
	// the scope fields (ids, assignees, labels) and the AnyReplica escape
	// hatch — none of that is restated here, and this role changes none of it.
	//
	// FILTER.IDS IS NOT AN ERROR WHEN IT NAMES A NON-STALE OR UNKNOWN ISSUE.
	// Like GetManyRequest.IDs, an id that is not currently a stale lease
	// simply does not appear in ReclaimResult.Reclaimed — scoping by id
	// narrows a sweep, it does not assert that every named id was reclaimable.
	//
	// MORE THAN MaxReclaimIDs ENTRIES IN Filter.IDs IS A REFUSAL: a
	// *TooManyReclaimIDsError, counted as sent, before deduplication.
	Filter ReclaimFilter
}

// ReclaimResult reports every issue a reclaim reverted, in the order the sweep
// found them.
type ReclaimResult struct {
	// Reclaimed names every issue the sweep reverted, together with the owner
	// each lease was taken from and the fresh revision the revert minted on
	// each row. Never nil for a successful call, even when it is empty: a
	// sweep that finds nothing stale answers with an empty, non-nil slice, the
	// same promise GetManyResult.Issues makes.
	Reclaimed []ReclaimedLease
}

// LeaseReclaimer sweeps stale leases back to ready — the capability behind
// `bd reclaim` — and, like every other capability here, a role with its own
// accessor.
//
// IT IS A DIFFERENT QUESTION FROM Releaser. Releaser gives up ONE claim a
// caller names, on the claim's own say-so (Actor, or Force). This sweeps
// MANY leases that have gone stale by a clock, on nobody's say-so but the
// clock's — a reaper calls this, not the agent that held the work. A surface
// carrying both would let a caller that may only report its own abandoned
// work reach into every other holder's, which is the same split Releaser's
// own doc draws against Claimer.
//
// WRITES, AND ITS HOOK IS on_update, once per reverted row. A reclaim clears
// assignee and status on each row it reverts, which is an update and is
// already how the journal classifies it — see Releaser's identical
// reasoning. There is no on_reclaim to fire and inventing one is not this
// role's job.
//
// Reclaim only ever touches the permanent issues table: wisps are ephemeral
// and are never leased work (see internal/storage/issueops.
// ReclaimExpiredLeasesInTx). The replica guard ReclaimFilter.AnyReplica
// documents is unchanged by this role — it is enforced in the shared body
// every library leg calls through.
//
// EVERY LEG RUNS ONE BODY: dolt, embedded dolt and the unit-of-work provider
// wrap internal/storage/issueops.ExecuteReclaimInTx, and the HTTP client leg
// dials POST /v0/beads/issues:reclaim (capability issues.reclaim), behind which
// bd serve runs the same body. `bd reclaim` runs this role on both of its
// routes. Each leg records one version-control entry per sweep that reverted
// anything, and honors a deferred version commit on the context.
//
// Deterministic request-validation failures match ErrValidation. Result
// values are unspecified when error is non-nil, and every refusal below
// leaves persistent state unchanged.
type LeaseReclaimer interface {
	// Reclaim sweeps every stale lease ReclaimRequest.Filter admits whose
	// lease_expires_at is more than OlderThan in the past, and reports every
	// issue it reverted.
	//
	// IT IS ONE TRANSACTION over the whole sweep: the snapshot that decides
	// which leases are stale, every row's revert, and the revision each revert
	// mints all see one write. A lease a concurrent heartbeat rescues between
	// the snapshot and its row's revert is simply skipped — it never appears
	// in Reclaimed, and is not an error.
	//
	// REFUSALS:
	//
	//   - an empty or all-blank Actor: ErrValidation, before anything is read;
	//   - a negative OlderThan: ErrValidation;
	//   - more than MaxReclaimIDs entries in Filter.IDs, or an empty-string
	//     entry among them: *TooManyReclaimIDsError or ErrValidation,
	//     respectively, before anything is read;
	//   - an empty or all-blank entry in Filter.Assignees, Labels, LabelsAny or
	//     ExcludeLabels: ErrValidation, because it would match nothing a
	//     caller meant to name.
	//
	// Every ErrValidation refusal but the cap is a *ReclaimFieldError naming
	// its field.
	//
	// A sweep that finds no stale leases is not a refusal: it answers an empty,
	// non-nil Reclaimed.
	Reclaim(ctx context.Context, request ReclaimRequest) (ReclaimResult, error)
}
