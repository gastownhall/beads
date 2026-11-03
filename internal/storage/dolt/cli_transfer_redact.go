package dolt

import (
	"regexp"
	"strings"
)

// redactURLCredentials strips credentials from a single remote URL so it is
// safe to echo into a transfer error. CI commonly configures sync.remote as
// git+https://x-access-token:<token>@github.com/org/repo, and dolt echoes that
// stored URL back in rejection output.
//
// HTTP(S) userinfo is transport credentials and is dropped whole (a token can
// sit in the username alone). SSH userinfo selects the remote account, which is
// not a secret, so only a password component is dropped. Host, port and path
// are kept as spelled. scp-style remotes, plain remote names and URLs without
// userinfo are returned unchanged.
//
// This mirrors redactRemoteURL in cmd/bd/sync_remote.go, which package main
// keeps and storage cannot import; keep the two rules in step.
func redactURLCredentials(raw string) string {
	sep := strings.Index(raw, "://")
	if sep < 0 {
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

// transferURLWithUserinfoRe matches the "scheme://userinfo@host" span of a URL
// embedded anywhere in free text, stopping the host at the first "/" so the
// path stays outside the match. Same pattern as urlWithUserinfoRe in
// cmd/bd/sync_remote.go.
var transferURLWithUserinfoRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^@\s'"]*@[^/\s'"]*`)

// scrubTransferText redacts every credential-bearing URL embedded in s (dolt
// subprocess output, an operation naming a peer) with redactURLCredentials.
// Text with no "userinfo@" URL is returned unchanged, so credential-free
// diagnostics keep their full detail.
func scrubTransferText(s string) string {
	return transferURLWithUserinfoRe.ReplaceAllStringFunc(s, redactURLCredentials)
}
