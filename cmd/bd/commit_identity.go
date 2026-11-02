package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// Embedded commit identity (GH#7628): every store construction goes through
// an activating factory (see the note at the top of events_journal.go), and
// commit identity rides the same rail. The embedded store resolves its
// commit author from GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL first (server mode's
// rule), then from the fallback installed here — the resolved actor name and
// owner email — and only then from its own anonymous beads <beads@local>
// pair. Without the fallback, embedded commits lost who ran the command
// even when the CLI knew.

// activateCommitIdentityStore installs cliCommitIdentityFallback on an
// embedded store, mirroring activateEventsJournalStore's defer shape.
// storage.CommitIdentityConfigurer is an optional interface only the
// embedded store implements, so every other backend — and every
// non-embedded open in either build — passes through untouched. A store
// opened cross-workspace still gets the LAUNCHING invocation's identity:
// the fallback is the resolved actor of the bd process that writes, which
// is the same attribution server mode gives that process's commits.
func activateCommitIdentityStore(s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	if err != nil {
		return s, err
	}
	if c, ok := storage.UnwrapStore(s).(storage.CommitIdentityConfigurer); ok {
		c.SetCommitIdentityFallback(cliCommitIdentityFallback)
	}
	return s, nil
}

// cliCommitIdentityFallback is the CLI's contribution to the embedded
// store's commit identity: the actor the command resolved for its audit
// trail, and the owner email CV attribution uses. The store consults it
// only for a field GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL did not supply, and
// at most once per resolution (see SetCommitIdentityFallback).
func cliCommitIdentityFallback() (name, email string) {
	name = getActorWithGit()
	// The chain's terminal "unknown" is a sentinel for "no actor anywhere",
	// not an identity to attribute commits with: drop it and let the store
	// fall back to its anonymous pair. An "unknown" a source explicitly
	// names is a deliberate identity and survives.
	if name == "unknown" && !actorExplicitlyUnknown() {
		name = ""
	}
	email = getOwner()
	return name, email
}

// actorFlagChangedAtStartup records whether the operator passed --actor,
// captured at pre-run: the flag binds the same global the actor resolution
// later writes back into, so whether it was set has to outlive the parsed
// value. Reading rootCmd for it at fallback time instead would close a
// package initialization cycle (rootCmd's initializer reaches the store
// factories, which install this file's fallback), so the pre-run records
// the answer here.
var actorFlagChangedAtStartup bool

// actorExplicitlyUnknown reports whether an operator-controlled source of
// the actor chain is set to the literal "unknown": the --actor flag, the
// BEADS_ACTOR / BD_ACTOR environment variables, the config.yaml `actor`
// key, git user.name, or $USER. Those are the only rungs that can make the
// resolved actor "unknown" deliberately; every other resolution ends in the
// sentinel this function exists to distinguish it from.
func actorExplicitlyUnknown() bool {
	// --actor: the pre-run recorded that the flag was passed, and a
	// non-empty flag value short-circuits the rest of the chain
	// (resolveActorWithGit returns it as-is), so a resolved "unknown"
	// under a passed flag can only have come from the flag.
	if actorFlagChangedAtStartup && getActorWithGit() == "unknown" {
		return true
	}
	if os.Getenv("BEADS_ACTOR") == "unknown" || os.Getenv("BD_ACTOR") == "unknown" {
		return true
	}
	if config.GetString("actor") == "unknown" {
		return true
	}
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if strings.TrimSpace(string(out)) == "unknown" {
			return true
		}
	}
	return os.Getenv("USER") == "unknown"
}