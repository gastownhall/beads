package dolt

import (
	"context"
	"database/sql"
)

// preCommitHookKey is the private context key for the create-replay test
// seam below. Production contexts never carry a value under it.
type preCommitHookKey struct{}

// withPreCommitHook returns a context whose write transactions run hook in
// commitWriteTxOn — after the transaction body has completed successfully
// and before the SQL commit — on every attempt of a retried write.
//
// This is a private test seam for the deterministic create-replay regression
// tests (GH#7289): it lets a test interleave a real concurrent writer while
// the losing transaction is complete but still open, so the loser's own
// commit conflict — not an injected error — drives withRetryTx's replay.
// The hook must not commit or roll back the handed transaction; returning an
// error fails that attempt through the normal transaction error path
// (rollback plus the closure's error classification). No public API sets a
// hook, and nothing outside this package can.
func withPreCommitHook(ctx context.Context, hook func(tx *sql.Tx) error) context.Context {
	return context.WithValue(ctx, preCommitHookKey{}, hook)
}

// preCommitHookFrom returns the pre-commit hook ctx carries, if any.
func preCommitHookFrom(ctx context.Context) (func(tx *sql.Tx) error, bool) {
	hook, ok := ctx.Value(preCommitHookKey{}).(func(tx *sql.Tx) error)
	if !ok || hook == nil {
		return nil, false
	}
	return hook, true
}
