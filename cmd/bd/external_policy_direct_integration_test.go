//go:build cgo && unix

package main

import "testing"

func TestProxiedServerExternalPolicyDirectParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newDirectHistoryProject(t, bd, "xd")
	exerciseExternalMutationPolicy(t, crossModeEnv{mode: "direct-server", bd: bd, dir: p.dir, env: p.env})
}
