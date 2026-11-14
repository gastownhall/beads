package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
)

// checkRemotePolicy refuses a transfer with the named remote when the
// effective federation.allowed-remote-patterns list is non-empty and the
// remote's registered URL does not satisfy it (GH#6233). It runs before any
// route is chosen, any credential is applied and any write wrapper is entered,
// so a refused remote transfers nothing, including one registered before the
// list was tightened.
//
// An empty list returns before any lookup. Otherwise the URL is resolved the
// way the transfer routes resolve it: the server's dolt_remotes listing first,
// then the remotes persisted on disk that the server is proven to serve
// (PersistedRemoteInfos, the GH#2118 cold-start window). A failed listing or
// a name neither source knows is refused: not being able to say where a
// transfer goes is not permission to send it there.
func (s *DoltStore) checkRemotePolicy(ctx context.Context, name string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	return s.checkRemotePolicyWith(ctx, name, patterns)
}

func (s *DoltStore) checkRemotePolicyWith(ctx context.Context, name string, patterns []string) error {
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
	for _, r := range s.PersistedRemoteInfos() {
		if r.Name == name {
			return remotecache.CheckRemotePolicy(subject, r.URL, patterns)
		}
	}
	return &remotecache.RemotePolicyError{Subject: subject, Reason: "remote not found, so its URL cannot be checked against " + remotecache.AllowedRemotePatternsKey}
}
