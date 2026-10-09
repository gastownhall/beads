package main

import (
	"github.com/steveyegge/beads/internal/storage"
)

// THE ATTRIBUTION OMISSIONS (S6d).
//
// A store that reports storage.CallerAttributionLimitedStore (today the http
// backend) refuses two caller-authored members its wire cannot carry, and
// would otherwise refuse two everyday commands outright. The CLI omits them
// for such a store only where the omission loses nothing a reader relies on:
//
//   - `bd reopen`'s Provenance. The CLI's label is the fixed "bd: reopen <id>",
//     fully derivable from the operation and the id; the server records its
//     own reopen entry for the same issue and the same actor. Nothing is lost.
//
//   - `bd update -s closed`'s closed_by_session. It is attribution only: no
//     bd or gc behavior (ready, close guards, claims) reads it, and the status
//     change, its close-policy guards and the atomicity with every other field
//     in the same update are all kept. It is also usually AMBIENT — every agent
//     under Claude Code exports CLAUDE_SESSION_ID — so refusing the close
//     would break the command for an input the caller never typed. The close
//     still happens, and a one-line notice on stderr says the session was not
//     recorded — for an explicit --session and for the ambient
//     CLAUDE_SESSION_ID alike, so the omission is never silent — pointing at
//     `bd close`, whose wire operation does carry the session.
//
// No capability token is spent on either: the wire publishes neither member,
// so there is nothing to negotiate yet. When a wire revision publishes one,
// the store's CallerAttributionLimited answer becomes the handshake check and
// these call sites need no change.

// storeCarriesCallerAttribution reports whether s can record the caller's own
// history label and closed_by_session.
func storeCarriesCallerAttribution(s storage.DoltStorage) bool {
	if s == nil {
		return true
	}
	limited, ok := storage.UnwrapStore(s).(storage.CallerAttributionLimitedStore)
	return !ok || !limited.CallerAttributionLimited()
}

// reopenProvenance is `bd reopen`'s history label for store, or "" (the
// backend's own label) where the store cannot carry one.
func reopenProvenance(store storage.DoltStorage, id string) string {
	if !storeCarriesCallerAttribution(store) {
		return ""
	}
	return "bd: reopen " + id
}

// storeIsRemoteBackend reports whether s is a pure network client of a remote
// bd serve (storage.RemoteBackendStore), seen through the decorator chain.
func storeIsRemoteBackend(s storage.DoltStorage) bool {
	if s == nil {
		return false
	}
	remote, ok := storage.UnwrapStore(s).(storage.RemoteBackendStore)
	return ok && remote.IsRemoteBackendStore()
}
