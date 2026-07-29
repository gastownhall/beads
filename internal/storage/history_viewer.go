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
