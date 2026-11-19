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

	// PreviousExternalRefs returns, for each of ids, the external_ref value
	// recorded by the most recent commit at or before asOf. A tracker pull
	// asks it once for all its linked issues at lastSync (GH#6605).
	//
	// An id is absent from the map when no comparable history entry exists
	// for it at or before asOf: the issue may have been created after asOf,
	// or the history that held it may have been compacted, flattened or
	// otherwise rewritten (#7294). Absence says nothing about whether the
	// ref changed; callers decide how to treat it. A present empty string is
	// a historical row whose external_ref column was NULL. Empty and repeated
	// ids are ignored. A query failure returns err and no map, never a
	// partial answer. Nothing is cached between calls.
	PreviousExternalRefs(ctx context.Context, ids []string, asOf time.Time) (map[string]string, error)
}
