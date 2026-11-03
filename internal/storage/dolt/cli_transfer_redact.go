package dolt

import (
	"regexp"
	"strings"
)

// redactURLCredentials strips credentials from a remote URL so it is safe to
// echo into a CLI transfer error. CI commonly configures sync.remote as
// git+https://x-access-token:<token>@github.com/org/repo, and a failed dolt
// transfer echoes that URL into the error this package wraps.
//
// This mirrors redactRemoteURL in cmd/bd/sync_remote.go (package main is not
// an importable dependency of storage): HTTP(S) userinfo is transport
// credentials and is dropped whole; SSH userinfo selects the remote account
// (git@host is not a secret), so only a password component is dropped.
func redactURLCredentials(raw string) string {
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
	if isTransferSSHScheme(scheme) {
		if colon := strings.Index(userinfo, ":"); colon >= 0 {
			userinfo = userinfo[:colon]
		}
		return scheme + "://" + userinfo + "@" + host + tail
	}
	return scheme + "://" + host + tail
}

func isTransferSSHScheme(scheme string) bool {
	return scheme == "ssh" || scheme == "git+ssh"
}

// transferURLWithUserinfoRe matches the "scheme://userinfo@host" span of a
// URL embedded anywhere in free-form text — dolt's combined push/pull output
// can echo the very remote (credentials and all) that it failed to reach.
// The userinfo run stops at the first "@" and the host run at the first "/"
// so a trailing path is left for redactURLCredentials to keep. This is the
// same pattern as urlWithUserinfoRe in cmd/bd/sync_remote.go.
var transferURLWithUserinfoRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^@\s'"]*@[^/\s'"]*`)

// scrubTransferText redacts credential-bearing URLs embedded anywhere in s by
// running each match through redactURLCredentials, so the per-scheme rule
// (http(s) drops userinfo, ssh drops only a password) also covers URLs that
// arrive inside dolt's arbitrary diagnostic text. Text with no "userinfo@"
// URL is returned unchanged, so credential-free dolt output keeps its full
// detail.
func scrubTransferText(s string) string {
	return transferURLWithUserinfoRe.ReplaceAllStringFunc(s, redactURLCredentials)
}