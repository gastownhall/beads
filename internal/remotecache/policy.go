package remotecache

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltremote"
)

// AllowedRemotePatternsKey is the config key that holds the remote allowlist.
const AllowedRemotePatternsKey = "federation.allowed-remote-patterns"

// ErrRemoteNotAllowed is the sentinel every allowlist refusal wraps, so a
// caller can tell a policy refusal from a transport or lookup failure with
// errors.Is (GH#6233).
var ErrRemoteNotAllowed = errors.New("remote not permitted by " + AllowedRemotePatternsKey)

// RemotePolicyError is an allowlist refusal. It names the thing that was
// refused (a remote name such as "origin", or a config key such as
// "sync.remote") and why, but never the URL, the patterns, or a parser error:
// a remote URL can carry credentials, and a refusal is printed, logged and
// emitted as JSON.
type RemotePolicyError struct {
	// Subject names what was refused: `remote "origin"`, `sync.remote`, ...
	Subject string
	// Reason says why; empty means the destination matched no pattern.
	Reason string
}

func (e *RemotePolicyError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "its URL matches no " + AllowedRemotePatternsKey + " entry"
	}
	return fmt.Sprintf("%s is not permitted: %s (see `bd config get %s`)", e.Subject, reason, AllowedRemotePatternsKey)
}

// Unwrap lets errors.Is(err, ErrRemoteNotAllowed) identify the refusal.
func (e *RemotePolicyError) Unwrap() error { return ErrRemoteNotAllowed }

// RemoteSubject is the conventional RemotePolicyError subject for a named
// Dolt remote or federation peer.
func RemoteSubject(name string) string {
	return fmt.Sprintf("remote %q", name)
}

// PolicyDestination is the form of a remote URL the allowlist is evaluated
// against: the Dolt form bd registers and hands to Dolt (doltremote.Normalize).
// A typed https://github.com/org/repo and the git+https://github.com/org/repo
// Dolt stores for it are the same destination, so they get one verdict.
// Patterns are not normalized: they keep literal path.Match semantics, so a
// forge destination needs a git+ pattern (git+https://github.com/org/*).
func PolicyDestination(rawURL string) string {
	return doltremote.Normalize(rawURL)
}

// CheckRemotePolicy decides whether rawURL may be used as a remote under
// patterns (the effective federation.allowed-remote-patterns value).
//
// An empty pattern list is no policy: nil, whatever rawURL is, so a workspace
// without an allowlist keeps exactly the behavior (and the structural checks)
// it had. A non-empty list, even one holding only blank or malformed entries,
// is a policy: the destination must pass ValidateRemoteURLWithPatterns in its
// PolicyDestination form, and a malformed entry matches nothing. A refusal is
// a *RemotePolicyError naming subject, never the URL.
func CheckRemotePolicy(subject, rawURL string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	if rawURL == "" {
		return &RemotePolicyError{Subject: subject, Reason: "its URL could not be resolved, and " + AllowedRemotePatternsKey + " is set"}
	}
	dest := PolicyDestination(rawURL)
	if err := ValidateRemoteURL(dest); err != nil {
		return &RemotePolicyError{Subject: subject, Reason: "its URL is not a valid remote URL"}
	}
	if err := ValidateRemoteURLWithPatterns(dest, patterns); err != nil {
		return &RemotePolicyError{Subject: subject}
	}
	return nil
}

// CheckProposedRemote is CheckRemotePolicy for a remote about to be
// registered, adopted or applied as name, under the effective
// federation.allowed-remote-patterns value. Callers run it before they write
// or remove anything, so a refused URL leaves the existing records as they
// were.
func CheckProposedRemote(name, rawURL string) error {
	return CheckRemotePolicy(RemoteSubject(name), rawURL, config.AllowedRemotePatterns())
}
