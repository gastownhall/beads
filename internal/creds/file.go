package creds

import "context"

// FileSource reads a static password from the beads credentials file, keyed by
// [host:port]. The file itself is owned by internal/configfile; this rung carries
// only the endpoint key and an injected Lookup, so the engine stays dependency-free —
// the same shape as EnvSource, which reads an env var without owning env-var policy.
//
// It is the lowest *configured* rung and is opportunistic: a missing or unreadable
// file, or an endpoint with no entry, is indistinguishable from "not set here" and
// falls through to the next rung (ultimately the driver-native default). It never
// fails the ladder — fail-closed is reserved for rungs the operator configured
// explicitly (a command), not an ambient machine-wide file.
type FileSource struct {
	Host   string                             // endpoint host for the [host:port] key
	Port   int                                // endpoint port
	Lookup func(host string, port int) string // returns "" for not-found/any error
	Label  string                             // provenance slug; defaults to "credentials-file"
}

// Name returns the provenance slug.
func (s FileSource) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return "credentials-file"
}

// Resolve looks up the password for the endpoint. It reports not-configured (and
// never calls Lookup) when the endpoint is missing — the DSN had no TCP host:port
// (a unix socket, or an unparseable DSN) — or when the file has no matching entry.
// The result is always a KindSecret: the credentials file carries only a password.
func (s FileSource) Resolve(_ context.Context) (Credential, bool, error) {
	if s.Lookup == nil || s.Host == "" || s.Port <= 0 {
		return Credential{}, false, nil
	}
	pw := s.Lookup(s.Host, s.Port)
	if pw == "" {
		return Credential{}, false, nil
	}
	return Credential{Value: pw, Kind: KindSecret, Source: s.Name()}, true, nil
}
