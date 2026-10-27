package dberrors

import "strings"

// IsRemoteDanglingReference reports whether err is Dolt's remote-side
// dangling-reference push refusal ("Error 1105 (HY000): unknown push error;
// Found dangling references to HashSet {…}", GH#3358). Dolt's remote chunk
// store raises it from chunk_store.go's errorIfDangling: before moving the
// remote root it asks the remote's HasChunks whether every direct ref of the
// working set / new store root is already there, and any absent ref aborts the
// push with this message. The check runs before the remote branch is updated
// (tryCommitChunks), so when this classification fires the remote branch did
// not move.
//
// The match is on the lower-cased message text, so it survives every tier that
// flattens the error: the SQL route's 1105 envelope, the CLI route's
// CombinedOutput rendering, the embedded engine, and the http tier's
// text-only errors.
//
// Two other error families deliberately do NOT match, and callers must not
// loosen these exclusions:
//
//   - bd's own local pre-push fsck refusal ("dangling chunk reference:
//     aborting push to prevent propagating corrupt chunks: …") is LOCAL
//     corruption the push never reached the remote with; recovery guidance
//     pointing at the remote would mislead the user.
//
//   - Dolt's local NBS store form ("dangling ref: found dangling references
//     to HashSet {…}") can come from the local store as well as a file-remote
//     sink, so it is not classified as a remote refusal.
func IsRemoteDanglingReference(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// bd's local pre-push fsck abort, quoted fsck findings included: a push
	// that never reached the remote must not get remote-shaped guidance.
	if strings.Contains(msg, "dangling chunk reference") {
		return false
	}
	// Dolt's remote chunk store says "found dangling references to"; the
	// NBS store's local form prefixes "dangling ref: ". Only a message with
	// more of the former than the latter is a remote-side refusal.
	return strings.Count(msg, "found dangling references to") >
		strings.Count(msg, "dangling ref: found dangling references to")
}
