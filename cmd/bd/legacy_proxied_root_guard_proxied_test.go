//go:build cgo

package main

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
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

// TestProxiedServerLegacyRootGuardRefusesReleasedBD pins the point of the
// marker against a real older release: run in a workspace the current build
// initialized, a released bd whose default proxied-server root is
// .beads/proxieddb (v1.1.0 through v1.2.x) refuses with "not a directory"
// instead of reporting an empty, freshly created database.
//
// The released bd is v1.1.0 from the @bd_releases catalog (sha256-pinned,
// declared as test data; see releasedBD1110).
func TestProxiedServerLegacyRootGuardRefusesReleasedBD(t *testing.T) {
	requireProxiedServerEnv(t)
	released := releasedBD1110(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "lrr")
	created := bdProxiedCreate(t, bd, p.dir, "Real issue an older bd must not hide", "--type", "task")
	// Without the marker the released bd would start a proxy for its own
	// root; stop it so a failing run leaves nothing behind.
	t.Cleanup(func() { _ = proxy.Shutdown(legacyRootPath(p.beadsDir)) })

	// A missing marker is reported after the runs below, so a failure
	// shows what the released bd does without it.
	marker, markerErr := os.ReadFile(legacyRootPath(p.beadsDir))
	for _, args := range [][]string{{"list", "--json"}, {"stats"}} {
		out, err := bdProxiedRun(t, released, p.dir, args...)
		if err == nil {
			t.Errorf("released bd %v exited 0 in a workspace it cannot read:\n%s", args, out)
			continue
		}
		if !strings.Contains(string(out), legacyProxiedRootName+": not a directory") {
			t.Errorf("released bd %v error lacks %q:\n%s", args, legacyProxiedRootName+": not a directory", out)
		}
		if strings.Contains(string(out), "panic:") || strings.Contains(string(out), "goroutine ") {
			t.Errorf("released bd %v panicked:\n%s", args, out)
		}
		if after, err := os.ReadFile(legacyRootPath(p.beadsDir)); markerErr == nil && (err != nil || !bytes.Equal(after, marker)) {
			t.Fatalf("released bd %v changed the legacy-root marker (err=%v)", args, err)
		}
	}
	assertLegacyRootArmed(t, p.dir, p.beadsDir)

	found := false
	for _, issue := range bdProxiedListJSON(t, bd, p) {
		if issue.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("current bd lost %s after the released bd ran", created.ID)
	}
}

// releasedBD1110Env names the runfile holding the released bd v1.1.0: the
// @bd_releases catalog's sha256-verified pin, set from cmd/bd/BUILD.bazel.
const releasedBD1110Env = "BEADS_TEST_BD_V1110_BIN"

// releasedBD1110 resolves the released bd v1.1.0 for this run. Under Bazel a
// missing or unusable runfile fails the test, so a target that forgets the
// wiring cannot pass by skipping; outside Bazel there is no pinned binary and
// the test skips. The catalog pins linux/amd64 release assets only.
func releasedBD1110(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("the release catalog pins linux/amd64 assets only; no released bd v1.1.0 runs on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !bazeltest.IsBazel() {
		t.Skip("the released bd v1.1.0 is a Bazel runfile (@bd_releases//:v1.1.0); run under bazel test")
	}
	path, err := bazeltest.RunfileEnv(releasedBD1110Env)
	if err != nil {
		t.Fatalf("released bd v1.1.0: %v", err)
	}
	return path
}
