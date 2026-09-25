//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCreateRepoProxiedTargetError exercises the CLI boundary: the target
// store's typed error must survive create --repo and reach --json callers.
func TestEmbeddedCreateRepoProxiedTargetError(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}

	bd := buildEmbeddedBD(t)
	directDir, _, _ := bdInit(t, bd, "--prefix", "direct")
	before, err := bdRunWithFlockRetry(t, bd, directDir, "list", "--json")
	if err != nil {
		t.Fatalf("list before create: %v: %s", err, before)
	}
	proxiedDir := t.TempDir()
	proxiedBeadsDir := filepath.Join(proxiedDir, ".beads")
	if err := os.MkdirAll(proxiedBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxiedMetadata := []byte(`{"database":"p","backend":"dolt","dolt_mode":"proxied-server"}`)
	if err := os.WriteFile(filepath.Join(proxiedBeadsDir, "metadata.json"), proxiedMetadata, 0o644); err != nil {
		t.Fatal(err)
	}

	runBD := func(envelope string, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = directDir
		cmd.Env = append(envWithout(bdEnv(directDir), "BD_JSON_ENVELOPE"), "BD_JSON_ENVELOPE="+envelope)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}
	runCreate := func(target, envelope string) (string, string, error) {
		return runBD(envelope, "create", "--json", "--repo", target, "x")
	}

	stdout, stderr, err := runCreate(proxiedDir, "0")
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("proxied create exit = %v, want 1; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("proxied create stderr = %q, want empty", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("proxied create stdout is not JSON: %q: %v", stdout, err)
	}
	if got["code"] != "proxy.store.unrouted" || got["reason"] != "unimplemented" ||
		got["mutates"] != false || got["schema_version"] != float64(1) {
		t.Fatalf("proxied create JSON = %v", got)
	}
	if message, ok := got["error"].(string); !ok || !strings.Contains(message, proxiedBeadsDir) ||
		!strings.Contains(message, "proxied-server workspace") || strings.Contains(message, "this command has no proxied-server route") {
		t.Fatalf("proxied create error misidentifies the target: %v", got["error"])
	}

	// The same typed refusal must keep its fields under data when the
	// supported JSON envelope is enabled.
	stdout, stderr, err = runCreate(proxiedDir, "1")
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("enveloped create exit = %v, want 1; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("enveloped create stderr = %q, want empty", stderr)
	}
	var wrapped map[string]any
	if err := json.Unmarshal([]byte(stdout), &wrapped); err != nil {
		t.Fatalf("enveloped create stdout is not JSON: %q: %v", stdout, err)
	}
	data, ok := wrapped["data"].(map[string]any)
	if !ok || wrapped["schema_version"] != float64(1) || data["code"] != "proxy.store.unrouted" ||
		data["reason"] != "unimplemented" || data["mutates"] != false {
		t.Fatalf("enveloped create JSON = %v", wrapped)
	}
	textCmd := exec.Command(bd, "create", "--repo", proxiedDir, "x")
	textCmd.Dir = directDir
	textCmd.Env = bdEnv(directDir)
	textOut, textErr, textExit := runCommandBuffers(t, textCmd)
	if exitErr, ok := textExit.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("text create exit = %v, want 1", textExit)
	}
	if textOut.Len() != 0 || !strings.Contains(textErr.String(), proxiedBeadsDir) ||
		!strings.Contains(textErr.String(), "proxied-server workspace") || strings.Contains(textErr.String(), "this command has no proxied-server route") {
		t.Fatalf("text create output lacks target context: stdout=%q stderr=%q", textOut.String(), textErr.String())
	}
	assertTypedRefusal := func(label, stdout, stderr string, runErr error) {
		t.Helper()
		if exitErr, ok := runErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			t.Fatalf("%s exit = %v, want 1; stdout=%q stderr=%q", label, runErr, stdout, stderr)
		}
		if stderr != "" {
			t.Fatalf("%s stderr = %q, want empty", label, stderr)
		}
		var refusal map[string]any
		if err := json.Unmarshal([]byte(stdout), &refusal); err != nil {
			t.Fatalf("%s stdout is not JSON: %q: %v", label, stdout, err)
		}
		message, _ := refusal["error"].(string)
		if refusal["code"] != "proxy.store.unrouted" || refusal["reason"] != "unimplemented" ||
			refusal["mutates"] != false || refusal["schema_version"] != float64(1) ||
			!strings.Contains(message, proxiedBeadsDir) || strings.Contains(message, "--repo") {
			t.Fatalf("%s refusal = %v", label, refusal)
		}
	}

	// Automatic routing reaches the same store factory without a --repo flag.
	if out, stderr, err := runBD("0", "config", "set", "routing.default", proxiedDir); err != nil {
		t.Fatalf("set routing.default: %v; stdout=%q stderr=%q", err, out, stderr)
	}
	stdout, stderr, err = runBD("0", "create", "--json", "x")
	assertTypedRefusal("auto-routed create", stdout, stderr, err)
	if out, stderr, err := runBD("0", "config", "unset", "routing.default"); err != nil {
		t.Fatalf("unset routing.default: %v; stdout=%q stderr=%q", err, out, stderr)
	}

	// Parent lookup for dry-run opens a preview store through a separate path.
	stdout, stderr, err = runBD("0", "create", "--dry-run", "--json", "--parent", "direct-missing", "--repo", proxiedDir, "x")
	assertTypedRefusal("dry-run create", stdout, stderr, err)

	// An ordinary target-store failure must retain the existing text/stderr
	// behavior even when the caller requested JSON.
	badDir := t.TempDir()
	badBeadsDir := filepath.Join(badDir, ".beads")
	if err := os.MkdirAll(badBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badBeadsDir, "metadata.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runCreate(badDir, "0")
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("bad metadata create exit = %v, want 1; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "Error: failed to open target store:") {
		t.Fatalf("bad metadata output: stdout=%q stderr=%q", stdout, stderr)
	}
	stdout, stderr, err = runBD("0", "create", "--dry-run", "--json", "--parent", "direct-missing", "--repo", badDir, "x")
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("bad metadata dry-run exit = %v, want 1; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "Error: failed to open target store for dry-run:") {
		t.Fatalf("bad metadata dry-run output: stdout=%q stderr=%q", stdout, stderr)
	}
	entries, err := os.ReadDir(proxiedBeadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "metadata.json" {
		t.Fatalf("proxied target gained files: %v", entries)
	}
	metadataAfter, err := os.ReadFile(filepath.Join(proxiedBeadsDir, "metadata.json"))
	if err != nil || !bytes.Equal(metadataAfter, proxiedMetadata) {
		t.Fatalf("proxied target metadata changed: %v", err)
	}
	after, err := bdRunWithFlockRetry(t, bd, directDir, "list", "--json")
	if err != nil {
		t.Fatalf("list after create: %v: %s", err, after)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("failed creates changed direct workspace: before=%s after=%s", before, after)
	}
}
