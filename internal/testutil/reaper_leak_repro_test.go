//go:build !windows

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// createdContainerIDs returns the set of container IDs currently sitting in
// docker's `created` state — created on the daemon but never started.
func createdContainerIDs(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-a", "--filter", "status=created", "-q", "--no-trunc").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	ids := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		ids[l] = true
	}
	return ids
}

// TestReaperFailureLeaksContainer reproduces ga-rv0rh9.
//
// testcontainers-go v0.43.0 calls ContainerCreate (docker.go:1285) BEFORE it
// connects the reaper (docker.go:1331). When the reaper fails, CreateContainer
// returns (non-nil container, error) — deliberately, so the caller can clean
// up — and dolt.Run faithfully passes that non-nil container through.
//
// beads' helpers drop it on the floor, so the created-but-never-started
// container is orphaned forever: state `created`, State.Error empty, and
// nothing in the dockerd journal, because ContainerStart was never issued.
//
// Here the reaper is broken by pointing the docker socket bind-mount at a
// regular file, so Ryuk cannot talk to the daemon and its wait strategy fails.
func TestReaperFailureLeaksContainer(t *testing.T) {
	if state := checkDolt(); state != doltReady {
		t.Skipf("skipping: %s", state)
	}

	// A regular file is a valid bind-mount source but not a docker socket.
	bogusSocket := filepath.Join(t.TempDir(), "not-a-docker.sock")
	if err := os.WriteFile(bogusSocket, []byte{}, 0o600); err != nil {
		t.Fatalf("write bogus socket: %v", err)
	}
	t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", bogusSocket)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "false") // reaper must be ENABLED

	before := createdContainerIDs(t)

	provider, err := NewContainerProvider()
	if err == nil {
		if provider != nil {
			_ = provider.Stop()
		}
		t.Skip("reaper did not fail; cannot exercise the leak path on this host")
	}
	t.Logf("NewContainerProvider failed as expected: %v", err)

	after := createdContainerIDs(t)

	var leaked []string
	for id := range after {
		if !before[id] {
			leaked = append(leaked, id)
		}
	}

	// Never leave the host dirtier than we found it, whether we pass or fail.
	t.Cleanup(func() {
		for _, id := range leaked {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	})

	for _, id := range leaked {
		out, _ := exec.Command("docker", "inspect", "-f",
			"{{.State.Status}}|{{.State.Error}}|{{.Config.Image}}|{{index .Config.Labels \"org.testcontainers.reap\"}}",
			id).Output()
		t.Logf("LEAKED %s -> %s", id[:12], strings.TrimSpace(string(out)))
	}

	if len(leaked) > 0 {
		t.Fatalf("ga-rv0rh9: %d container(s) leaked in `created` state after a reaper failure; "+
			"the helper must call testcontainers.TerminateContainer(ctr) on the dolt.Run error path", len(leaked))
	}
}
