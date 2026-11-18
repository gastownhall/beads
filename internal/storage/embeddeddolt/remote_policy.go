//go:build cgo

package embeddeddolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
)

// checkRemotePolicy refuses a transfer with the named remote when the
// effective federation.allowed-remote-patterns list is non-empty and the
// remote's registered URL does not satisfy it (GH#6233). An empty list returns
// before any lookup; a failed lookup or an unknown name is refused, since not
// knowing where a transfer goes is not permission to send it there.
func (s *EmbeddedDoltStore) checkRemotePolicy(ctx context.Context, name string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	subject := remotecache.RemoteSubject(name)
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return &remotecache.RemotePolicyError{Subject: subject, Reason: "its URL could not be looked up to check it against " + remotecache.AllowedRemotePatternsKey}
	}
	for _, r := range remotes {
		if r.Name == name {
			return remotecache.CheckRemotePolicy(subject, r.URL, patterns)
		}
	}
	return &remotecache.RemotePolicyError{Subject: subject, Reason: "remote not found, so its URL cannot be checked against " + remotecache.AllowedRemotePatternsKey}
}

// commitPendingBeforeTransfer is the pre-transfer commit a remote verb makes
// (CommitPending), taken only once the allowlist admits remote (GH#6233): a
// refused transfer leaves local history as it was, as server mode's does.
// what names the transfer in a commit error ("push", "pull", "sync").
// withPeerAuth checks the remote again before it dispatches the transfer.
func (s *EmbeddedDoltStore) commitPendingBeforeTransfer(ctx context.Context, remote, what string) error {
	if err := s.checkRemotePolicy(ctx, remote); err != nil {
		return err
	}
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return fmt.Errorf("commit pending before %s: %w", what, err)
	}
	return nil
}
