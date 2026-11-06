package storage

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// HistoryViewer provides time-travel queries and diffs.
type HistoryViewer interface {
	History(ctx context.Context, issueID string) ([]*HistoryEntry, error)
	AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error)
	Diff(ctx context.Context, fromRef, toRef string) ([]*DiffEntry, error)
}

// ExternalRefHistoryQuerier is implemented by history-capable Dolt storage
// backends that can resolve what a given issue's external_ref was as of a
// point in time, by querying Dolt's dolt_history_issues system table.
//
// This is intentionally a separate capability from RawDBAccessor
// (storage.go): a backend can have Dolt's history tables without exposing a
// raw *sql.DB (EmbeddedDoltStore executes all SQL through its own
// transaction-scoped connection helper rather than a pooled *sql.DB), and a
// hypothetical non-Dolt SQL backend could conceivably expose a *sql.DB
// (e.g. for diagnostics) without ever having dolt_history_issues. Gating
// dolt_history_issues queries on this capability -- rather than on DB()
// presence -- keeps the fast path tied to the thing it actually depends on.
type ExternalRefHistoryQuerier interface {
	HistoryViewer

	// PreviousExternalRef returns the external_ref value recorded for
	// issueID as of the most recent commit at or before asOf.
	//
	// found is false when no history entry exists for issueID at or
	// before asOf (e.g. the issue was created after asOf); callers should
	// treat that as "changed" since there is nothing to compare against.
	// ref is the empty string when the historical row's external_ref
	// column was NULL.
	PreviousExternalRef(ctx context.Context, issueID string, asOf time.Time) (ref string, found bool, err error)
}

// ExternalRefHistoryBatchQuerier is the batch form of
// ExternalRefHistoryQuerier: one call answers PreviousExternalRef's question
// for many issue ids, so a caller looping over linked issues (an incremental
// tracker pull, GH#6605) reads history once per pull instead of issuing one
// dolt_history_issues query per issue — each of which costs seconds on a
// mature repository.
//
// Embedding ExternalRefHistoryQuerier is deliberate: a batch-capable store
// can always serve the single-id question too, and callers that fall back to
// per-issue reads on a batch failure keep answering through the exact
// capability whose semantics the batch result must match.
type ExternalRefHistoryBatchQuerier interface {
	ExternalRefHistoryQuerier

	// PreviousExternalRefs returns, for each id in issueIDs, the
	// external_ref value recorded as of the most recent commit at or before
	// asOf — the same answer PreviousExternalRef gives for that id.
	//
	// Presence in the returned map carries the single-id call's found: an
	// id absent from the map has no history entry at or before asOf, and a
	// present empty string means the historical row's external_ref column
	// was NULL or empty. Duplicate and empty ids are ignored; empty input
	// returns an empty map without touching history. Ids with no history
	// are simply not present — the caller decides what "not found" means.
	PreviousExternalRefs(ctx context.Context, issueIDs []string, asOf time.Time) (map[string]string, error)
}
