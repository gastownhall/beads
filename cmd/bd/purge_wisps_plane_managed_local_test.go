//go:build cgo && unix

package main

import (
	"testing"
	"time"
)

// TestManagedLocalProxiedPurgeWispsPlaneRetention runs the purge retention
// scenario on the managed-local proxied topology, the default for a proxied
// workspace bd starts the Dolt server for. Named TestManagedLocalProxied* so
// the proxied-local smoke lane runs it.
func TestManagedLocalProxiedPurgeWispsPlaneRetention(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "pwl", 5*time.Minute)
	runPurgeRetentionScenario(t, proxiedPurgeScenarioRunner(bd, p))
}
