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

// WithImmediateVersionCommit returns a context that creates its Dolt version
// commit even when an ancestor context deferred it.
//
// A route that applies the deferral once, for every command it dispatches —
// the proxied-server CLI sets it on the root context (GH#4995) — needs this to
// re-express the exemption the per-verb routes get by not being wrapped at all:
// an explicit commit point ("commit these writes now, with this message") is
// not a write whose versioning is policy, so it must not inherit the deferral.
func WithImmediateVersionCommit(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferVersionCommitKey{}, false)
}

// VersionCommitDeferred reports whether ctx defers version commits.
func VersionCommitDeferred(ctx context.Context) bool {
	deferred, _ := ctx.Value(deferVersionCommitKey{}).(bool)
	return deferred
}
