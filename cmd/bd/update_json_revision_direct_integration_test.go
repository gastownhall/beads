//go:build cgo && unix

package main

import "testing"

func TestProxiedServerUpdateJSONRevisionDirect(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newDirectHistoryProject(t, bd, "urd")
	exerciseUpdateJSONRevision(t, crossModeEnv{mode: "direct-server", bd: bd, dir: p.dir, env: p.env})
}
