//go:build cgo

package main

import (
	"testing"
)

// TestProxiedServerLegacyRootGuardArmsOnInit pins GH#5104 end to end: a
// default managed-local proxied workspace in a Git worktree gets the
// git-ignored legacy-root marker from its init, and the current build keeps
// reading and writing its real issues with the marker in place.
func TestProxiedServerLegacyRootGuardArmsOnInit(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "lrg")
	assertLegacyRootArmed(t, p.dir, p.beadsDir)

	created := bdProxiedCreate(t, bd, p.dir, "Real issue behind the legacy-root marker", "--type", "task")
	found := false
	for _, issue := range bdProxiedListJSON(t, bd, p) {
		if issue.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("bd list did not return %s with the legacy-root marker in place", created.ID)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "stats"); err != nil {
		t.Fatalf("bd stats with the legacy-root marker in place: %v\n%s", err, out)
	}
	assertLegacyRootArmed(t, p.dir, p.beadsDir)
}
