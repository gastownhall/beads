//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCreateUserConfigYamlDarwinLegacyFIFOSeedsWithoutOpening: a FIFO at the
// legacy path is not a regular file, so the carry-over never opens it; an
// implementation that did would block on the read (GH#8113: the Application
// Support location may hold anything, and the probe must never hang).
func TestCreateUserConfigYamlDarwinLegacyFIFOSeedsWithoutOpening(t *testing.T) {
	home, nativeDir := darwinCreateSeamForTest(t)
	countLegacyProbes(t)
	path := mkUserConfigDir(t, home)

	legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := syscall.Mkfifo(legacyPath, 0o600); err != nil {
		t.Fatalf("mkfifo legacy path: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(legacyPath) })

	done := make(chan error, 1)
	seed := []byte("metrics:\n  disabled: false\n")
	go func() { done <- CreateUserConfigYaml(path, seed) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreateUserConfigYaml blocked on the legacy FIFO; a non-regular legacy file must never be opened")
	}
	if got := readCreatedUserConfig(t, path); got != string(seed) {
		t.Errorf("created config = %q, want seed %q (the FIFO must not be read)", got, seed)
	}
}
