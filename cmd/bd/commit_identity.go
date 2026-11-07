package main

import (
	"github.com/steveyegge/beads/internal/storage"
)

// activateCommitIdentityStore gives a store that authors its own Dolt commits
// (storage.CommitIdentityConfigurer: the embedded store) bd's resolved actor
// and owner as the identity to commit under when GIT_AUTHOR_NAME /
// GIT_AUTHOR_EMAIL leave a field empty (GH#7628). It passes a nil store or an
// error through, peels decorators, and only installs a callback: the store
// calls it on its first commit, so a read-only command never resolves the
// actor or runs git for it.
//
// Every store factory registers it as its LAST deferred activation, so it runs
// first and the activations after it see the configured store.
func activateCommitIdentityStore(s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	if err != nil || s == nil {
		return s, err
	}
	if configurer, ok := storage.UnwrapStore(s).(storage.CommitIdentityConfigurer); ok {
		configurer.SetCommitIdentityFallback(commitIdentityFallback)
	}
	return s, nil
}

// commitIdentityFallback is the actor (--actor > BEADS_ACTOR > BD_ACTOR >
// config actor > git user.name > $USER) as the name and the owner (git
// user.email) as the email. The last-resort "unknown" actor, which no source
// named, is no identity, so the store keeps its default author; an "unknown"
// a source names on purpose is kept.
func commitIdentityFallback() (name, email string) {
	name = getActor()
	if name == "" || name == "unknown" {
		// getActor's "" means no fallback ran; "unknown" may be the last
		// resort. Either way the sources decide, with no last resort.
		if sourced := actorFromSources(); name == "" || sourced == "" {
			name = sourced
		}
	}
	return name, getOwner()
}
