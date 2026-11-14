package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233: federation.allowed-remote-patterns used to be read only by
// `bd config validate`, so an out-of-policy remote synced unchecked. These
// helpers are the enforcement half on the server store: every covered
// transfer resolves the remote name to its effective URL and applies the
// shared policy gate before any transport runs.

// checkRemotePolicy enforces federation.allowed-remote-patterns for the
// remote NAME a transfer would use, resolving the name to the URL the
// transfer would dial. An empty allowlist returns before any lookup: the
// setting is opt-in and costs nothing when absent.
//
// Resolution respects the transfer routes' evidence precedence: the
// SQL-visible remotes first, then the remotes persisted on disk that this
// store's server provably serves (PersistedRemoteInfos, the GH#2118
// cold-start window). Under a non-empty list an unresolved name refuses:
// inability to resolve a destination is not authorization to use it. A
// listing error refuses the same way rather than failing open.
func (s *DoltStore) checkRemotePolicy(ctx context.Context, remote string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	url, err := s.remoteURLForPolicy(ctx, remote)
	if err != nil {
		return err
	}
	return remotecache.ValidateRemoteWithPolicy(patterns, remotePolicySubject(remote), url)
}

// remotePolicySubject names the refused destination in a policy error: the
// remote or peer name, without its URL.
func remotePolicySubject(remote string) string {
	return fmt.Sprintf("remote %q", remote)
}

// checkRemoteURLPolicy enforces the allowlist for a URL a caller is about to
// register (AddRemoteWithRef, AddFederationPeer): no name resolution, the
// destination is in hand. Registration gates refuse before any record is
// written, so a refused proposal leaves prior records untouched.
func (s *DoltStore) checkRemoteURLPolicy(subject, url string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	return remotecache.ValidateRemoteWithPolicy(patterns, subject, url)
}

// remoteURLForPolicy resolves the URL a transfer with remote name would use.
// See checkRemotePolicy for the precedence and refusal rules.
func (s *DoltStore) remoteURLForPolicy(ctx context.Context, remote string) (string, error) {
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return "", &remotecache.RemotePolicyError{
			Subject: remotePolicySubject(remote),
			Cause:   fmt.Sprintf("the Dolt remotes could not be listed to enforce the allowlist: %v", err),
		}
	}
	for _, r := range remotes {
		if r.Name == remote {
			return r.URL, nil
		}
	}
	for _, r := range s.PersistedRemoteInfos() {
		if r.Name == remote {
			return r.URL, nil
		}
	}
	return "", &remotecache.RemotePolicyError{
		Subject: remotePolicySubject(remote),
		Cause:   "its URL could not be resolved to enforce the allowlist (the remote is not registered)",
	}
}
