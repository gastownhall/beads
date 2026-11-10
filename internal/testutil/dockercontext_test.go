//go:build !windows

package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinDockerHostFromContext_RespectsExistingValue(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///explicit/choice.sock")
	PinDockerHostFromContext()
	if got := os.Getenv("DOCKER_HOST"); got != "unix:///explicit/choice.sock" {
		t.Fatalf("DOCKER_HOST = %q, want the pre-set value untouched", got)
	}
}

func TestPinDockerHostFromContext_ResolvesActiveContext(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho 'unix:///stub/context.sock'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("writing docker stub: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("DOCKER_HOST", "")
	_ = os.Unsetenv("DOCKER_HOST")

	PinDockerHostFromContext()
	if got := os.Getenv("DOCKER_HOST"); got != "unix:///stub/context.sock" {
		t.Fatalf("DOCKER_HOST = %q, want the stub context endpoint", got)
	}
}

func TestPinDockerHostFromContext_NoDockerIsANoOp(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no docker binary reachable
	t.Setenv("DOCKER_HOST", "")
	_ = os.Unsetenv("DOCKER_HOST")

	PinDockerHostFromContext()
	if got, set := os.LookupEnv("DOCKER_HOST"); set {
		t.Fatalf("DOCKER_HOST = %q, want unset when docker is absent", got)
	}
}
