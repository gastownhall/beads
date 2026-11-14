//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// GH#6233: federation.allowed-remote-patterns used to be read only by
// `bd config validate`, so an out-of-policy remote synced unchecked. Embedded
// mode routes every remote verb through withPeerAuth (Push, Pull, the
// --remote and strategy variants, Fetch, PushTo, PullFrom, Sync's fetch and
// push steps), so the enforcement gate placed at its top covers the whole
// surface with one check before any transfer runs.

// checkPeerPolicy enforces federation.allowed-remote-patterns for the remote
// or peer NAME an embedded remote verb would use, resolving the name to the
// URL the verb would dial. An empty allowlist returns before any lookup: the
// setting is opt-in and costs nothing when absent.
//
// Resolution follows the verb's own evidence order: the dolt_remotes row the
// engine would dial first, then the federation_peers row (a peer registered
// without a live dolt remote still names its destination). Under a non-empty
// list an unresolved name refuses — inability to resolve a destination is not
// authorization to use it — and so does a lookup error, rather than failing
// open.
func (s *EmbeddedDoltStore) checkPeerPolicy(ctx context.Context, peer string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	url, err := s.peerURLForPolicy(ctx, peer)
	if err != nil {
		return err
	}
	return remotecache.ValidateRemoteWithPolicy(patterns, remotePolicySubject(peer), url)
}

// remotePolicySubject names the refused destination in a policy error: the
// remote or peer name, without its URL. Same shape as package dolt's helper
// so the two backends' refusals read identically.
func remotePolicySubject(remote string) string {
	return fmt.Sprintf("remote %q", remote)
}

// checkRemoteURLPolicy enforces the allowlist for a URL a caller is about to
// register (AddRemoteWithRef, AddFederationPeer): no name resolution, the
// destination is in hand. Registration gates refuse before any record is
// written, so a refused proposal leaves prior records untouched.
func (s *EmbeddedDoltStore) checkRemoteURLPolicy(subject, url string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	return remotecache.ValidateRemoteWithPolicy(patterns, subject, url)
}

// peerURLForPolicy resolves the URL a remote verb with this name would dial.
// See checkPeerPolicy for the precedence and refusal rules.
func (s *EmbeddedDoltStore) peerURLForPolicy(ctx context.Context, peer string) (string, error) {
	var url string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT url FROM dolt_remotes WHERE name = ?", peer).Scan(&url)
	})
	if err == nil {
		return url, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", &remotecache.RemotePolicyError{
			Subject: remotePolicySubject(peer),
			Cause:   fmt.Sprintf("the dolt remotes could not be read to enforce the allowlist: %v", err),
		}
	}
	// No dolt remote under this name: a federation_peers row still names
	// where the verb would go (AddFederationPeer can outlive its dolt remote).
	var row *issueops.FederationPeerRow
	err = s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		row, err = issueops.GetFederationPeerInTx(ctx, tx, peer)
		return err
	})
	if err == nil {
		return row.Peer.RemoteURL, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return "", &remotecache.RemotePolicyError{
			Subject: remotePolicySubject(peer),
			Cause:   "its URL could not be resolved to enforce the allowlist (the remote is not registered)",
		}
	}
	return "", &remotecache.RemotePolicyError{
		Subject: remotePolicySubject(peer),
		Cause:   fmt.Sprintf("the federation peers could not be read to enforce the allowlist: %v", err),
	}
}
