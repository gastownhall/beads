package storage

import (
	"context"
	"time"
)

// LeaseRef names a lease to renew, keyed on the row's claim_fence at the
// caller's snapshot. Renewal renews only while the fence still matches, so a
// claim whose ownership moved (reclaim, transfer, release) since the snapshot
// is reported lost instead of silently renewed.
type LeaseRef struct {
	ID    string
	Fence int64
}

// LeaseRenewalOutcome is the typed per-ref result of a batch renewal.
type LeaseRenewalOutcome string

const (
	// LeaseRenewed: the lease was pushed forward.
	LeaseRenewed LeaseRenewalOutcome = "renewed"
	// LeaseRenewalLost: the row exists but the fence no longer matches — the
	// claim's ownership moved out from under the caller.
	LeaseRenewalLost LeaseRenewalOutcome = "lost"
	// LeaseRenewalNotFound: no row with that id.
	LeaseRenewalNotFound LeaseRenewalOutcome = "not_found"
	// LeaseRenewalUnleased: the row is owned and in progress but carries no
	// lease (lease.auto off, or claimed before the lease stack). Renewal
	// never arms a lease as a side effect.
	LeaseRenewalUnleased LeaseRenewalOutcome = "unleased"
)

// LeaseRenewalResult is the outcome for one ref in a batch renewal.
type LeaseRenewalResult struct {
	ID      string              `json:"id"`
	Outcome LeaseRenewalOutcome `json:"outcome"`
}

// DefaultRenewalChunkSize bounds how many leases a single renewal transaction
// rewrites: renewal rewrites row_lock on every renewed row, so an unbounded
// batch would collide with any concurrent worker write on any of its rows and
// replay the whole batch (a livelock surface at fleet scale). Chunking caps
// the blast radius per transaction.
const DefaultRenewalChunkSize = 64

// LeaseRenewer is the store surface RenewLeasesChunked drives.
type LeaseRenewer interface {
	RenewLeases(ctx context.Context, refs []LeaseRef, ttl time.Duration) ([]LeaseRenewalResult, error)
}

// RenewLeasesChunked renews refs in bounded chunks (one RenewLeases call —
// one transaction — per chunk) so a single renewal tick never rewrites
// row_lock on an unbounded set and livelocks against concurrent worker
// writes. chunkSize <= 0 uses DefaultRenewalChunkSize. It is a store-agnostic
// helper, not a store method, so no implementer has to duplicate the loop. On
// a chunk error it returns the outcomes accumulated from the fully-processed
// chunks so far, plus the error — callers must not assume len(out)==len(refs)
// on error.
func RenewLeasesChunked(ctx context.Context, s LeaseRenewer, refs []LeaseRef, ttl time.Duration, chunkSize int) ([]LeaseRenewalResult, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultRenewalChunkSize
	}
	out := make([]LeaseRenewalResult, 0, len(refs))
	for start := 0; start < len(refs); start += chunkSize {
		end := start + chunkSize
		if end > len(refs) {
			end = len(refs)
		}
		got, err := s.RenewLeases(ctx, refs[start:end], ttl)
		if err != nil {
			return out, err
		}
		out = append(out, got...)
	}
	return out, nil
}
