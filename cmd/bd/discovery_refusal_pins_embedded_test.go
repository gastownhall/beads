//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEmbeddedDiscoveryPinsEnvFileReadonly is the store-backed member of the
// before-parity discovery pins (discovery_refusal_pins_test.go holds the
// store-less ones). It pins how an ordinary workspace applies a BD_READONLY
// supplied by its own .beads/.env: a write is refused with the read-only text
// and exit 1, also under --json, while reads still run; and a BD_READONLY the
// caller exports wins over the file.
func TestEmbeddedDiscoveryPinsEnvFileReadonly(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pin")
	if err := os.WriteFile(filepath.Join(beadsDir, ".env"), []byte("BD_READONLY=true\n"), 0o600); err != nil {
		t.Fatalf("write .beads/.env: %v", err)
	}

	run := func(extraEnv []string, args ...string) (exit int, stdout, stderr string) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		// The store-less pins' closed environment: no BD_*/BEADS_* the caller
		// exports (BD_READONLY, BD_JSON_ENVELOPE, BD_OTEL_*, ...) reaches bd.
		// HOME stays dir, where bd init ran.
		cmd.Env = append(discoveryPinEnv(dir, filepath.Join(dir, "no-such-freeze-marker")),
			"BEADS_DOLT_AUTO_START=0", "BEADS_NO_DAEMON=1")
		cmd.Env = append(cmd.Env, extraEnv...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run bd %v: %v", args, err)
			}
			exit = exitErr.ExitCode()
		}
		return exit, out.String(), discoveryPinSandboxNotice.ReplaceAllLiteralString(errOut.String(), "")
	}

	exit, stdout, stderr := run(nil, "create", "refused by .env", "--json")
	if exit != 1 || stdout != "" || stderr != "Error: operation 'create' is not allowed in read-only mode\n" {
		t.Fatalf("create under .env BD_READONLY=true: exit=%d stdout=%q stderr=%q; want exit=1, no stdout, the read-only refusal", exit, stdout, stderr)
	}

	exit, stdout, stderr = run(nil, "list", "--json")
	if exit != 0 || stdout != "[]\n" {
		t.Fatalf("list under .env BD_READONLY=true: exit=%d stdout=%q stderr=%q; want exit=0 and an empty list", exit, stdout, stderr)
	}

	exit, stdout, stderr = run([]string{"BD_READONLY=false"}, "create", "allowed by the caller", "--json")
	if exit != 0 {
		t.Fatalf("create with exported BD_READONLY=false: exit=%d stdout=%q stderr=%q; want the caller's setting to win", exit, stdout, stderr)
	}
	var created struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || created.Title != "allowed by the caller" {
		t.Fatalf("create with exported BD_READONLY=false printed %q (decode error %v)", stdout, err)
	}

	exit, stdout, stderr = run(nil, "list", "--json")
	var listed []struct {
		Title string `json:"title"`
	}
	if exit != 0 {
		t.Fatalf("list after the allowed create: exit=%d stderr=%q", exit, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed) != 1 || listed[0].Title != "allowed by the caller" {
		t.Fatalf("list after the allowed create = %q (decode error %v); want only the allowed issue, the refused create wrote nothing", stdout, err)
	}
}
