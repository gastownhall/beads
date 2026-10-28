//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// `bd dolt start` on a workspace whose server a setting declares externally
// managed must refuse before it starts anything: a bd-owned server there
// serves .beads/dolt on the external endpoint's port, a second database that
// answers in place of the real one (GH#6585). These tests run the real bd
// binary against the recording dolt shim of init_external_autostart_test.go,
// so any start attempt shows as a shim invocation and nothing is left running.

// directExternalStartWorkspace writes the GH#6585 workspace into f's project:
// server mode at 127.0.0.1 on port, declared by metadata.json's
// dolt_server_port. Nothing listens on port.
func directExternalStartWorkspace(t *testing.T, f *externalInitFixture, port int) string {
	t.Helper()
	beadsDir := namedPortWorkspace(t, f, port)
	meta, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	meta.DoltServerHost = "127.0.0.1"
	if err := meta.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// externalStartRefusal is the --json shape of a typed capability refusal.
type externalStartRefusal struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Mutates bool   `json:"mutates"`
}

// requireExternalStartRefusal checks that res is the typed refusal, naming
// each of want, and that bd neither ran dolt nor left a server's pid or port
// file in beadsDir.
func requireExternalStartRefusal(t *testing.T, res externalInitResult, beadsDir string, want ...string) externalStartRefusal {
	t.Helper()
	if !res.failed {
		t.Errorf("bd dolt start succeeded on an externally managed workspace:\n%s", res.output)
	}
	var got externalStartRefusal
	if err := json.Unmarshal([]byte(firstJSONObject(res.output)), &got); err != nil {
		t.Fatalf("no JSON refusal in the output: %v\n%s", err, res.output)
	}
	if got.Code != "external.dolt_start.conflict" || got.Reason != "design" || got.Mutates {
		t.Errorf("refusal = {code:%q reason:%q mutates:%v}, want {external.dolt_start.conflict design false}:\n%s",
			got.Code, got.Reason, got.Mutates, res.output)
	}
	for _, w := range want {
		if !strings.Contains(got.Error, w) {
			t.Errorf("refusal does not name %q: %q", w, got.Error)
		}
	}
	requireNoLaunch(t, res)
	for _, name := range []string{"dolt-server.pid", "dolt-server.port"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s exists after a refused start (stat: %v)", name, err)
		}
	}
	return got
}

// TestDoltStartRefusesExternallyManagedServer is the GH#6585 shape:
// metadata.json's dolt_server_port declares the server external, and
// `bd dolt start` refuses with a typed error naming that setting and the
// endpoint, instead of starting bd's own server on the port.
func TestDoltStartRefusesExternallyManagedServer(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	beadsDir := directExternalStartWorkspace(t, f, port)

	res := f.run("", "--json", "dolt", "start")
	requireExternalStartRefusal(t, res, beadsDir,
		"dolt_server_port in "+filepath.Join(beadsDir, "metadata.json"),
		tcpEndpoint(port).addr)
}

// TestDoltStartExternalRefusalIgnoresForceAndAutoStart covers the starts the
// bd-7pkvvd fresh-data-dir refusal lets through: --force, and data already
// under .beads/dolt. Whatever dolt.auto-start says, the externally managed
// workspace gets the same refusal, and its text points at neither the start
// nor the --force it refuses.
func TestDoltStartExternalRefusalIgnoresForceAndAutoStart(t *testing.T) {
	for _, autoStart := range []string{"", "0", "1"} {
		for _, force := range []bool{false, true} {
			for _, existingDB := range []bool{false, true} {
				name := "auto-start unset"
				if autoStart != "" {
					name = "auto-start " + autoStart
				}
				args := []string{"--json", "dolt", "start"}
				if force {
					name += ", --force"
					args = append(args, "--force")
				}
				if existingDB {
					name += ", existing database"
				}
				t.Run(name, func(t *testing.T) {
					f := newExternalInitFixture(t)
					port := freeLoopbackPort(t)
					beadsDir := directExternalStartWorkspace(t, f, port)
					if existingDB {
						if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "named_port", ".dolt"), 0o700); err != nil {
							t.Fatal(err)
						}
						// This bd's version witness, so the legacy-upgrade
						// guard admits the populated data directory.
						if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
							t.Fatal(err)
						}
					}
					res := f.run(autoStart, args...)
					got := requireExternalStartRefusal(t, res, beadsDir, tcpEndpoint(port).addr)
					for _, absent := range []string{"bd dolt start", "--force"} {
						if strings.Contains(got.Error, absent) {
							t.Errorf("refusal contains %q: %q", absent, got.Error)
						}
					}
				})
			}
		}
	}
}

// TestDoltStartExternalDeclarations covers the declarations other than
// metadata.json's port: a port environment variable, or
// BEADS_DOLT_SERVER_MODE=1 with config.yaml's dolt.port choosing the port.
// Each gets the same refusal, naming the variable that declared the server.
func TestDoltStartExternalDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envName string
		yaml    bool // dolt.port in config.yaml names the port, not the variable
		setting string
	}{
		{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_PORT", false, "BEADS_DOLT_SERVER_PORT declares the Dolt server at "},
		{"legacy BEADS_DOLT_PORT", "BEADS_DOLT_PORT", false, "BEADS_DOLT_PORT declares the Dolt server at "},
		{"BEADS_DOLT_SERVER_MODE=1", "BEADS_DOLT_SERVER_MODE", true, "BEADS_DOLT_SERVER_MODE=1 declares this workspace's Dolt server externally managed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalInitFixture(t)
			port := freeLoopbackPort(t)
			beadsDir := namedPortWorkspace(t, f, 0)
			value := strconv.Itoa(port)
			if tc.yaml {
				namedPortConfigYaml(t, beadsDir, port)
				value = "1"
			}
			f.env = append(f.env, tc.envName+"="+value)

			res := f.run("", "--json", "dolt", "start")
			requireExternalStartRefusal(t, res, beadsDir, tc.setting, tcpEndpoint(port).addr)
		})
	}
}
