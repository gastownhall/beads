package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The sidecar-first read in GetStringFromDir deliberately applies to EVERY key
// read through the directory path, not only the machine-local list that
// `bd config set` routes there.
//
// That is the second half of #6443: the open path resolves pool keys through
// GetStringFromDir, never through Initialize+GetString, so before this a
// workspace could have one answer at the CLI and a different one inside the
// store open. dolt.pool-read-timeout is the worked example — not a machine-local
// key, so it pins the scope rather than the key list.
func TestSidecarPrecedenceAppliesToEveryDirectoryRead(t *testing.T) {
	const key = "dolt.pool-read-timeout"
	if IsMachineLocalKey(key) {
		t.Fatalf("%s must NOT be machine-local, or this test no longer pins the scope", key)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(key+": 10s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LocalConfigFileName), []byte(key+": 300s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := GetStringFromDir(dir, key); got != "300s" {
		t.Errorf("GetStringFromDir(%s) = %q, want the sidecar's \"300s\": the directory read must resolve the same value the CLI does", key, got)
	}

	// With no sidecar, the tracked file is still the answer. Written NESTED
	// on purpose: the sidecar reader accepts the flat dotted form, but
	// config.yaml here is walked by a nested-only reader, so a flat
	// `dolt.pool-read-timeout: 10s` would read as unset. That asymmetry is a
	// separate gap in the same function, not something this PR changes.
	bare := t.TempDir()
	if err := os.WriteFile(filepath.Join(bare, "config.yaml"), []byte("dolt:\n  pool-read-timeout: 10s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GetStringFromDir(bare, key); got != "10s" {
		t.Errorf("GetStringFromDir(%s) = %q, want \"10s\" when no sidecar exists", key, got)
	}
}
