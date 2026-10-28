//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
