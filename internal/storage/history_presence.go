package storage

import "context"

// HistoryPresence reports which of the given issue ids appear anywhere in the
// CURRENT branch's committed history of the issues table — that is, in some
// commit reachable from HEAD. A rewound-away past does not count, and neither
// does the uncommitted working set.
//
// That asymmetry is the whole point. "In this store's history but not in this
// store now" is safe to read as "a deletion this store recorded", while "not in
// this store's history and not in this store now" is indistinguishable from a
// torn, replaced, or rewound store — the corruption class GH#4988's auto-export
// orphan guard exists to catch. Auto-export uses the first as proof that a
// JSONL-only record was really deleted, and keeps refusing to overwrite on the
// second (GH#5896).
//
// It is a capability interface: callers should type-assert after UnwrapStore
// and degrade to the conservative path when a store does not implement it.
type HistoryPresence interface {
	// HistoricalIssueIDs returns the subset of ids that appear in HEAD's
	// committed history. Ids the store has never seen are simply absent from
	// the result; an empty or nil ids slice returns an empty set and issues
	// no query.
	HistoricalIssueIDs(ctx context.Context, ids []string) (map[string]struct{}, error)
}
