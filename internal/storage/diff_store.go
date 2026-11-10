package storage

import "context"

// DiffStore computes the set of issue IDs whose underlying data differs
// between two Dolt commits. It's a capability interface — callers should
// type-assert after UnwrapStore and fall back to a non-incremental path
// when a store doesn't implement it.
//
// "Underlying data" includes changes to the issues table itself as well as
// its label, dependency, and comment rows. An issue ID is reported under
// Upserted if any of those rows were added or modified, and under Removed
// only if the issue row itself was deleted. Label/dependency/comment row
// deletions are reported as Upserted (the issue survived, its relational
// data shrank).
type DiffStore interface {
	ChangedIssueIDs(ctx context.Context, fromCommit, toCommit string) (ChangedIssueIDs, error)
}

// ChangedIssueIDs lists the issue IDs whose stored representation differs
// between two commits.
type ChangedIssueIDs struct {
	Upserted []string
	Removed  []string
}
