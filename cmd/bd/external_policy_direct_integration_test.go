//go:build cgo && unix

package main

import "testing"

func TestProxiedServerExternalPolicyDirectParity(t *testing.T) {
	requireSharedProxiedServer(t)
	// This fixture drops its database and purges the shared server's dropped
	// databases on cleanup. Finish before parallel proxied fixtures start so
	// that server-wide cleanup cannot race their schema initialization.
	bd := buildEmbeddedBD(t)
	p := newDirectHistoryProject(t, bd, "xd")
	exerciseExternalMutationPolicy(t, crossModeEnv{mode: "direct-server", bd: bd, dir: p.dir, env: p.env})
}
