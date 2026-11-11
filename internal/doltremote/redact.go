package doltremote

import (
	"regexp"
	"strings"
)

// Redact strips credentials from a remote URL so it is safe to echo into
// errors, hints, logs and JSON. CI commonly configures sync.remote as
// git+https://x-access-token:<token>@github.com/org/repo, and Dolt echoes that
// stored URL back in its own push, pull and fetch errors.
//
// HTTP(S) userinfo is transport credentials and is dropped whole (a token can
// sit in the username alone). SSH userinfo selects the remote account (git@host
// is not a secret, and a hint is not runnable without it), so only a password
// component is dropped. Host, port and path are kept as spelled. scp-style
// remotes, plain remote names and URLs without userinfo are returned unchanged.
//
// This is the one copy of the rule: cmd/bd and the storage layer both call it,
// so a fix to it reaches every place a remote URL is echoed.
func Redact(raw string) string {
	sep := strings.Index(raw, "://")
	if sep < 0 {
		// scp-style (git@host:path) or a bare path: no place for a password.
		return raw
	}
	scheme, rest := raw[:sep], raw[sep+3:]
	authority, tail := rest, ""
	if slash := strings.Index(rest, "/"); slash >= 0 {
		authority, tail = rest[:slash], rest[slash:]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	userinfo, host := authority[:at], authority[at+1:]
	if scheme == "ssh" || scheme == "git+ssh" {
		if colon := strings.Index(userinfo, ":"); colon >= 0 {
			userinfo = userinfo[:colon]
		}
		return scheme + "://" + userinfo + "@" + host + tail
	}
	return scheme + "://" + host + tail
}

// urlWithUserinfoRe matches the "scheme://userinfo@host" span of a URL embedded
// anywhere in free text. The userinfo run takes quotes (a password can hold a
// raw "'") and reaches the authority's last "@", the split Redact makes, but it
// stops at a "/" or whitespace, so it never runs into the path or the next URL.
// The host run also stops at a quote, so the quotes a diagnostic wraps the URL
// in stay outside the match, and the path is left for Redact to keep.
var urlWithUserinfoRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s]*@[^/\s'"]*`)

// RedactText redacts every credential-bearing URL embedded in s (git or Dolt
// output, an error naming a remote) with Redact. Text with no "userinfo@" URL
// is returned unchanged, so credential-free diagnostics keep their full detail.
func RedactText(s string) string {
	return urlWithUserinfoRe.ReplaceAllStringFunc(s, Redact)
}

// RedactError is err with its text redacted by RedactText, unwrapping to err
// so errors.Is and errors.As still reach it, for an error that can quote a
// remote URL: Dolt's own push, pull and fetch errors quote the stored remote,
// credentials included. It is err itself when RedactText leaves the text as
// it is.
func RedactError(err error) error {
	if err == nil || RedactText(err.Error()) == err.Error() {
		return err
	}
	return redactedError{err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return RedactText(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }
