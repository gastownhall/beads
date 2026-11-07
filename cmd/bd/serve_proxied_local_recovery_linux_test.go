//go:build cgo && linux

package main

import (
	"testing"
	"time"
)

// TestManagedLocalProxiedServeRecoversAfterIdleExit is the managed-local twin
// of TestProxiedServerServeSurvivesProxyIdleGap: the same serve recovery
// contract of gastownhall/beads#7295, driven against the topology
// `bd init --proxied-server` builds with no external flags — bd supervises
// the loopback proxy and the proxy supervises a local dolt sql-server child —
// so the recovery is proven on the workspace shape a default `bd init` makes,
// not only on the shared-server harness.
func TestManagedLocalProxiedServeRecoversAfterIdleExit(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "srvrec", 2*time.Second)

	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
	t.Cleanup(func() { sp.shutdown(t) })

	requireServeRecoversAfterIdleGap(t, sp, p)
}
