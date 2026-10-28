//go:build cgo && unix

package main

// `bd dolt start` on a workspace whose Dolt server a setting declares
// externally managed must refuse — typed, before dolt is launched and before
// any start state is written (bd-pvh7lf). These tests run the real bd binary
// against the externalInitFixture's dolt shim, which records every launch, so
// a refusal is observable without a real server and leaves nothing running.

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// directExternalStartWorkspace writes the direct-external workspace the
// refusal tests start from: metadata.json dolt_mode server, dolt_server_host
// 127.0.0.1, dolt_server_port port — the shape `bd init --external` writes.
func directExternalStartWorkspace(t *testing.T, f *externalInitFixture, port int) string {
	t.Helper()
	beadsDir := namedPortWorkspace(t, f, port)
	meta, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata.json: %v", err)
	}
	meta.DoltServerHost = "127.0.0.1"
	if err := meta.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	return beadsDir
}

// requireStartRefused pins what every externally managed refusal shares: exit
// 1 and no dolt launch.
func requireStartRefused(t *testing.T, res externalInitResult) {
	t.Helper()
	if !res.failed {
		t.Fatalf("bd dolt start succeeded against an externally managed workspace:\n%s", res.output)
	}
	requireNoLaunch(t, res)
}

// externalStartRefusalCode pins the frozen code of the externally managed
// `bd dolt start` refusal as the wire contract downstream consumers parse,
// independently of the constant the command defines it with.
const externalStartRefusalCode = "external.dolt_start.conflict"

// requireStartRefusalJSON checks the typed refusal contract in res.output —
// code external.dolt_start.conflict, reason design, mutates false — and
// returns the payload for setting-specific assertions.
func requireStartRefusalJSON(t *testing.T, res externalInitResult) refusalPayload {
	t.Helper()
	payload, coded, _ := findRefusalJSON(res.output)
	if !coded {
		t.Fatalf("no typed refusal in output:\n%s", res.output)
	}
	if payload.Code != externalStartRefusalCode {
		t.Errorf("refusal code = %q, want %q:\n%s", payload.Code, externalStartRefusalCode, res.output)
	}
	if payload.Reason != string(ProxyReasonDesign) {
		t.Errorf("refusal reason = %q, want %q:\n%s", payload.Reason, ProxyReasonDesign, res.output)
	}
	if payload.Mutates {
		t.Errorf("refusal says it mutates:\n%s", res.output)
	}
	return payload
}

// TestDoltStartDirectExternalRefusal: a workspace whose metadata.json names
// dolt_server_port is externally managed, so `bd dolt start` refuses with the
// typed JSON refusal whose error names both the declaring setting and the
// endpoint it declares, leaving no start state behind.
func TestDoltStartDirectExternalRefusal(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	beadsDir := directExternalStartWorkspace(t, f, port)

	res := f.run("", "--json", "dolt", "start")
	requireStartRefused(t, res)
	payload := requireStartRefusalJSON(t, res)

	// The error names the declaring setting and the endpoint it declares.
	if !strings.Contains(payload.Error, "dolt_server_port in") ||
		!strings.Contains(payload.Error, filepath.Join(beadsDir, "metadata.json")) {
		t.Errorf("refusal does not name dolt_server_port in metadata.json:\n%s", payload.Error)
	}
	endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if !strings.Contains(payload.Error, endpoint) {
		t.Errorf("refusal does not name the declared endpoint %s:\n%s", endpoint, payload.Error)
	}

	// A refusal writes no start state: no pid or port file bd's later
	// commands would resolve through.
	for _, state := range []string{"dolt-server.pid", "dolt-server.port"} {
		if _, err := os.Stat(filepath.Join(beadsDir, state)); !os.IsNotExist(err) {
			t.Errorf("bd wrote %s although it refused the start", state)
		}
	}
}

// requireNoStartCommandAdvice fails when a refusal message points back at
// 'bd dolt start' — the command it refuses — or names --force, which does not
// bypass it.
func requireNoStartCommandAdvice(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "bd dolt start") {
		t.Errorf("refusal advises 'bd dolt start', the command it refuses:\n%s", text)
	}
	if strings.Contains(text, "--force") {
		t.Errorf("refusal names --force, which does not bypass it:\n%s", text)
	}
}

// TestDoltStartForceDoesNotBypassExternalRefusal: --force exists to bypass
// the fresh-data-dir refusal (bd-7pkvvd); it must not bypass the externally
// managed refusal — whatever BEADS_DOLT_AUTO_START says and however much
// database already sits under .beads/dolt, which is what the fresh-data-dir
// refusal keys on.
func TestDoltStartForceDoesNotBypassExternalRefusal(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	beadsDir := directExternalStartWorkspace(t, f, port)
	// A database under .beads/dolt is what the fresh-data-dir refusal keys
	// on, so the externally managed refusal must hold with one present. The
	// hand-made root needs a current-era version witness, or the legacy
	// upgrade guard refuses the workspace as a pre-1.0 one first.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "named_port", ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte("1.3.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, autoStart := range []string{"", "0", "1"} {
		t.Run("auto-start="+autoStart, func(t *testing.T) {
			res := f.run(autoStart, "--json", "dolt", "start", "--force")
			requireStartRefused(t, res)
			payload := requireStartRefusalJSON(t, res)
			requireNoStartCommandAdvice(t, payload.Error)
		})
	}
	// Text mode carries the same message.
	res := f.run("", "dolt", "start", "--force")
	requireStartRefused(t, res)
	if !strings.Contains(res.output, "refusing to start a Dolt server") {
		t.Errorf("text output does not name the refusal:\n%s", res.output)
	}
	requireNoStartCommandAdvice(t, res.output)
}

// TestDoltStartEnvDeclaredExternalRefusal: a workspace whose server only an
// environment variable declares externally managed — the port env vars, or
// BEADS_DOLT_SERVER_MODE=1 beside a config.yaml dolt.port — gets the same
// typed refusal, naming the variable.
func TestDoltStartEnvDeclaredExternalRefusal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		env        string // the full entry, %d standing in for the port
		configYaml bool   // pin dolt.port in .beads/config.yaml
		declaredBy string
	}{
		{name: "server port env", env: "BEADS_DOLT_SERVER_PORT=%d", declaredBy: "BEADS_DOLT_SERVER_PORT"},
		{name: "legacy port env", env: "BEADS_DOLT_PORT=%d", declaredBy: "BEADS_DOLT_PORT"},
		{name: "server mode env", env: "BEADS_DOLT_SERVER_MODE=1", configYaml: true, declaredBy: "BEADS_DOLT_SERVER_MODE=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalInitFixture(t)
			port := freeLoopbackPort(t)
			beadsDir := namedPortWorkspace(t, f, 0)
			if tc.configYaml {
				namedPortConfigYaml(t, beadsDir, port)
			}
			// The port is templated in with a plain Replace: Sprintf would append
			// %!(EXTRA ...) to the entry without a %d verb, silently breaking
			// the server-mode entry.
			f.env = []string{strings.Replace(tc.env, "%d", strconv.Itoa(port), 1)}

			res := f.run("", "--json", "dolt", "start")
			requireStartRefused(t, res)
			payload := requireStartRefusalJSON(t, res)
			if !strings.Contains(payload.Error, tc.declaredBy) {
				t.Errorf("refusal does not name the declaring setting %q:\n%s", tc.declaredBy, payload.Error)
			}
			if endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port)); !strings.Contains(payload.Error, endpoint) {
				t.Errorf("refusal does not name the declared endpoint %s:\n%s", endpoint, payload.Error)
			}
			for _, state := range []string{"dolt-server.pid", "dolt-server.port"} {
				if _, err := os.Stat(filepath.Join(beadsDir, state)); !os.IsNotExist(err) {
					t.Errorf("bd wrote %s although it refused the start", state)
				}
			}
		})
	}
}
