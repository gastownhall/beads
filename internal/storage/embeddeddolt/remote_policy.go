package embeddeddolt

import (
	"context"

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
