package issueops

import "context"

// deferVersionCommitKey marks a context whose guarded issue operations must
// leave their writes in the Dolt working set instead of creating a version
// commit.
type deferVersionCommitKey struct{}

// WithDeferredVersionCommit returns a context whose guarded issue operations
// skip the Dolt version commit they would otherwise create. The SQL write still
// commits; only the version commit is deferred to a later explicit commit point.
//
// `bd --dolt-auto-commit batch` needs this: batch mode exists to avoid one
// version commit per issue, and the commit point for the write verbs lives
// inside the storage layer, so the CLI has no commit message to blank the way
// it does on the RunInTransaction path.
func WithDeferredVersionCommit(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferVersionCommitKey{}, true)
}

// VersionCommitDeferred reports whether ctx defers version commits.
func VersionCommitDeferred(ctx context.Context) bool {
	deferred, _ := ctx.Value(deferVersionCommitKey{}).(bool)
	return deferred
}
