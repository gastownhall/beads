package remotecache

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

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
// against: the Dolt form bd registers and hands to Dolt for a forge URL
// (doltremote.Normalize). A typed https://github.com/org/repo and the
// git+https://github.com/org/repo Dolt stores for it are the same
// destination, so they get one verdict. Patterns are not normalized; see
// policySpellings for the spellings of the destination a pattern is matched
// against.
func PolicyDestination(rawURL string) string {
	return doltremote.Normalize(rawURL)
}

// policySpellings returns the spellings of a PolicyDestination that a
// pattern may match: the destination itself and, for a git+ URL, the same URL
// without the prefix. doltremote.Normalize writes every http(s):// and ssh://
// URL in git+ form, but bd registers a Dolt remotesapi endpoint
// (https://doltremoteapi.dolthub.com/org/repo, http://myserver:7007/mydb)
// unchanged (GH#3339), so a pattern written the way `bd dolt remote list`
// shows such a remote must match it. Both spellings name the same host and
// path, so accepting either widens nothing, and a typed URL and the form bd
// stores for it have the same spellings, so registration and transfer still
// get one verdict.
func policySpellings(dest string) []string {
	if rest, ok := strings.CutPrefix(dest, "git+"); ok {
		return []string{dest, rest}
	}
	return []string{dest}
}

// globUnsafeDestination reports whether dest has a part that path.Match's
// '*' accepts but that Dolt reads as more than a name under the pattern's
// prefix: a query (Dolt's s3:// factory sends signed requests to the
// endpoint= a query names), a fragment, or a ".." path segment
// (file:///srv/approved/.. is /srv). Dolt reads '\' in a path as a
// separator, and decodes %XX escapes when it parses the URL and once more in
// its file factory, so the segment check splits at both separators and runs
// on every decoding of dest: file:///srv/approved/%252e%252e opens /srv too.
func globUnsafeDestination(dest string) bool {
	if strings.ContainsAny(dest, "?#") {
		return true
	}
	// Each decoding that changes s makes it shorter, so this ends.
	for s := dest; ; {
		if hasDotDotSegment(s) {
			return true
		}
		decoded, err := url.PathUnescape(s)
		if err != nil {
			return true
		}
		if decoded == s {
			return false
		}
		s = decoded
	}
}

// hasDotDotSegment reports whether s, split at '/' and '\', has a ".."
// segment.
func hasDotDotSegment(s string) bool {
	for _, segment := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// globAllows reports whether pattern, a glob, allows dest, one spelling of a
// destination: pattern matches dest under MatchesRemotePattern, and in
// dest's authority (what follows scheme://, up to the next '/') each part
// between '@'s matches its own part of the pattern. path.Match lets '*' and
// '?' span an '@', so on its own gs://corp-*/* matches gs://corp-x@evil/db,
// where Dolt's gs, oci and http(s) remotes dial the host after the last '@'
// (bucket evil), and Dolt's aws://[bucket:table] host keeps only what
// follows an '@' in each name. A pattern therefore allows user info only
// where it spells the '@' itself: https://*@github.com/org/* allows
// https://user@github.com/org/repo, and an entry equal to the URL allows it
// as it is.
func globAllows(pattern, dest string) bool {
	if !MatchesRemotePattern(dest, pattern) {
		return false
	}
	// dest passed ValidateRemoteURL in its PolicyDestination form, so it is
	// scheme://authority[/path]: its third '/'-separated segment is the
	// authority.
	destSegments := strings.Split(dest, "/")
	if len(destSegments) < 3 || !strings.Contains(destSegments[2], "@") {
		return true
	}
	// '*', '?' and a plain character never match '/', so in a pattern with no
	// '\/' and no class that can match '/', the segment path.Match lined up
	// with the authority is the one at the same index. A pattern whose
	// segment count differs from dest's does not allow user info.
	patternSegments := strings.Split(pattern, "/")
	if len(patternSegments) != len(destSegments) {
		return false
	}
	destParts := strings.Split(destSegments[2], "@")
	patternParts := strings.Split(strings.ReplaceAll(patternSegments[2], `\@`, "@"), "@")
	if len(patternParts) != len(destParts) {
		return false
	}
	for i, part := range destParts {
		if !MatchesRemotePattern(part, patternParts[i]) {
			return false
		}
	}
	return true
}

// CheckRemotePolicy decides whether rawURL may be used as a remote under
// patterns (the effective federation.allowed-remote-patterns value).
//
// An empty pattern list is no policy: nil, whatever rawURL is, so a workspace
// without an allowlist keeps exactly the behavior (and the structural checks)
// it had. A non-empty list, even one holding only blank or malformed entries,
// is a policy: the destination must pass ValidateRemoteURL in its
// PolicyDestination form, and then an entry equal to one of its
// policySpellings allows it, or a pattern that allows a spelling as a glob
// (globAllows) does. A malformed entry matches nothing. The equality rule
// comes first because '[', ']', '*', '?' and '\' are glob syntax: an entry
// spelling a bracketed host (an IPv6 literal, Dolt's aws://[bucket:table])
// does not match that host as a glob. A destination with a query, a fragment
// or a ".." path segment is allowed only by an equal entry, since a glob over
// it does not bound where Dolt sends the data. A refusal is a
// *RemotePolicyError naming subject, never the URL.
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
	spellings := policySpellings(dest)
	for _, p := range patterns {
		if slices.Contains(spellings, p) {
			return nil
		}
	}
	if globUnsafeDestination(dest) {
		return &RemotePolicyError{Subject: subject, Reason: `its URL has a query, a fragment or a ".." path segment, which only an entry equal to the whole URL allows`}
	}
	for _, p := range patterns {
		for _, s := range spellings {
			if globAllows(p, s) {
				return nil
			}
		}
	}
	return &RemotePolicyError{Subject: subject}
}

// CheckProposedRemote is CheckRemotePolicy for a remote about to be
// registered, adopted or applied as name, under the effective
// federation.allowed-remote-patterns value. Callers run it before they write
// or remove anything, so a refused URL leaves the existing records as they
// were.
func CheckProposedRemote(name, rawURL string) error {
	return CheckRemotePolicy(RemoteSubject(name), rawURL, config.AllowedRemotePatterns())
}
