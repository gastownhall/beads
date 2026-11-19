//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedAuditConfigSetAndRecord pins the audit enablement path
// end-to-end (GH#5787): `bd config set audit.enabled true` is a documented,
// working key — it succeeds without the unrecognized-key warning, config
// get reports it back, and a subsequent `bd audit record` appends to the
// workspace's interactions sidecar.
func TestEmbeddedAuditConfigSetAndRecord(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "asc")

	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}

	stdout, stderr, err := run("config", "set", "audit.enabled", "true")
	if err != nil {
		t.Fatalf("bd config set audit.enabled failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if combined := stdout + stderr; strings.Contains(combined, "not a recognized config key") {
		t.Fatalf("bd config set audit.enabled must not warn about a recognized key; got:\n%s", combined)
	}

	stdout, stderr, err = run("config", "get", "audit.enabled")
	if err != nil {
		t.Fatalf("bd config get audit.enabled failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "true" {
		t.Fatalf("config get audit.enabled = %q, want %q", got, "true")
	}

	stdout, stderr, err = run("audit", "record", "--kind", "tool_call", "--tool-name", "probe")
	if err != nil {
		t.Fatalf("bd audit record failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("bd audit record printed no entry id")
	}

	sidecar := filepath.Join(beadsDir, "interactions.jsonl")
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read interactions sidecar: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one sidecar entry, got %d:\n%s", len(lines), raw)
	}
	if !strings.Contains(lines[0], "tool_call") {
		t.Fatalf("sidecar entry should record the tool_call kind; got:\n%s", lines[0])
	}
}
