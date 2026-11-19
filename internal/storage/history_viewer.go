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
// backends that can resolve what given issues' external_ref was as of a
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

	// PreviousExternalRefs resolves the external_ref recorded for every id
	// in issueIDs as of the most recent commit at or before asOf, reading
	// history a bounded number of times instead of once per issue.
	//
	// The returned map holds an entry only for the ids that have a
	// committed history row at or before asOf: an id absent from the map
	// may have been created after asOf, or the history that held it may
	// have been compacted, flattened or otherwise rewritten (#7294) — it
	// says nothing about whether the ref changed; callers decide how to
	// treat it. A present entry is the empty string when the historical
	// row's external_ref column was NULL. Empty ids are dropped,
	// duplicates collapse, and an empty issueIDs returns an empty map
	// without opening storage. A query failure returns err and no usable
	// map, never a partial answer.
	PreviousExternalRefs(ctx context.Context, issueIDs []string, asOf time.Time) (map[string]string, error)
}
