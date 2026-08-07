package testutil

import (
	"os"
	"os/exec"
	"strings"
)

// PinDockerHostFromContext resolves the Docker CLI's active context endpoint
// and pins it into DOCKER_HOST, for TestMains that are about to swap HOME.
//
// The active context lives in ~/.docker/config.json; once a TestMain points
// HOME at a temp dir, `docker info` (and testcontainers) silently falls back
// to unix:///var/run/docker.sock. On machines where the daemon is reached via
// a context (OrbStack, Docker Desktop's desktop-linux, remote daemons) that
// socket does not exist, so every container-gated test SKIPs with "Docker not
// available" even though docker works fine in the invoking shell (bd-84kos).
//
// Call it BEFORE the HOME swap. No-op when DOCKER_HOST is already set (an
// explicit choice always wins) or when the endpoint cannot be resolved.
func PinDockerHostFromContext() {
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	out, err := exec.Command("docker", "context", "inspect", "-f", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return
	}
	host := strings.TrimSpace(string(out))
	if host == "" {
		return
	}
	_ = os.Setenv("DOCKER_HOST", host)
}
