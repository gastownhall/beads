package dberrors

import "strings"

// IsRemoteDanglingReference reports Dolt's refusal to update a remote whose
// HasChunks denies having a chunk the pushed value points to: "unknown push
// error; Found dangling references to HashSet {…}" (GH#3358).
//
// Dolt raises it from (*DoltChunkStore).Put → errorIfDangling
// (libraries/doltcore/remotestorage/chunk_store.go), which runs for the
// working-set and store-root WriteValues of a push (store/datas/
// database_common.go) before tryCommitChunks moves the remote root, and
// PushToRemoteBranch wraps it as ErrUnknownPushErr
// (libraries/doltcore/env/actions/remotes.go). So the remote branch did not
// move, and the finding is about the remote, not the local database. Over the
// SQL route it arrives as Error 1105; `dolt push` forwards the same
// `call dolt_push(...)` to bd's sql-server, so the CLI route reports it too.
//
// The match is on the lowercased message, like IsAncestorPKMismatch, so it
// survives every tier that flattens errors to text (http, proxied, embedded,
// CLI CombinedOutput).
func IsRemoteDanglingReference(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "found dangling references to")
}
