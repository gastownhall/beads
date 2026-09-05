//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

type gateFrontDoorRunner struct {
	name string
	dir  string
	env  func(string) []string
}

func (r gateFrontDoorRunner) run(t *testing.T, bd string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env(r.dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

func (r gateFrontDoorRunner) mustRun(t *testing.T, bd string, args ...string) string {
	t.Helper()
	stdout, stderr, err := r.run(t, bd, args...)
	if err != nil {
		t.Fatalf("[%s] bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			r.name, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// TestGateFrontDoorDirectServerProxyParity keeps gate lifecycle writes on the
// same UOW front in direct SQL and proxied-server modes. Gate IDs are generated
// independently, so assertions compare stable lifecycle and blocking facts.
func TestGateFrontDoorDirectServerProxyParity(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	directProject := newServerModeProject(t, bd, "mg")
	proxiedProject := newSharedProxiedProject(t, bd, "mg")
	modes := []gateFrontDoorRunner{
		{name: "direct-sql", dir: directProject.dir, env: func(string) []string { return directProject.env }},
		{name: "proxied", dir: proxiedProject.dir, env: bdProxiedEnv},
	}

	for _, r := range modes {
		r.mustRun(t, bd, "create", "Gate target", "--id", "mg-target")
	}

	for _, r := range modes {
		created := r.mustRun(t, bd, "gate", "create", "--type=human", "--blocks", "mg-target", "--reason", "review required")
		gateID := parseCreatedGateID(t, created)
		for _, want := range []string{"Blocks: mg-target", "Reason: review required", "Resolve with: bd gate resolve " + gateID} {
			if !strings.Contains(created, want) {
				t.Errorf("[%s] gate create missing %q:\n%s", r.name, want, created)
			}
		}

		show := r.mustRun(t, bd, "gate", "show", gateID)
		for _, want := range []string{gateID, "Status: open", "Await Type: human", "Reason: review required"} {
			if !strings.Contains(show, want) {
				t.Errorf("[%s] gate show missing %q:\n%s", r.name, want, show)
			}
		}

		// A waiter is persisted by the same gate UOW and remains visible in
		// list output before resolution.
		r.mustRun(t, bd, "gate", "add-waiter", gateID, "worker1")
		var rows []map[string]any
		list := r.mustRun(t, bd, "gate", "list", "--all", "--json")
		if err := json.Unmarshal([]byte(strings.TrimSpace(list)), &rows); err != nil {
			t.Fatalf("[%s] parse gate list: %v\n%s", r.name, err, list)
		}
		var row map[string]any
		for _, candidate := range rows {
			if candidate["id"] == gateID {
				row = candidate
				break
			}
		}
		if row == nil || row["status"] != "open" {
			t.Fatalf("[%s] gate list row = %v, want open gate %s", r.name, row, gateID)
		}
		waiters, ok := row["waiters"].([]any)
		if !ok || len(waiters) != 1 || waiters[0] != "worker1" {
			t.Errorf("[%s] gate list waiters = %v, want [worker1]", r.name, row["waiters"])
		}

		resolved := r.mustRun(t, bd, "gate", "resolve", gateID, "--reason", "approved")
		if !strings.Contains(resolved, "Gate resolved: "+gateID) {
			t.Errorf("[%s] gate resolve output missing confirmation:\n%s", r.name, resolved)
		}
		closed := r.mustRun(t, bd, "gate", "show", gateID)
		if !strings.Contains(closed, "Status: closed") {
			t.Errorf("[%s] gate remains open after resolve:\n%s", r.name, closed)
		}

		// `gate check` is the watcher-facing read/transaction boundary. A
		// resolved human gate is a clean no-op and must still exit successfully.
		checked := r.mustRun(t, bd, "gate", "check", "--type", "human")
		// A closed gate is not an error; the watcher reports the explicit
		// no-open-gates result (or, on older clients, a resolved count).
		lowerChecked := strings.ToLower(strings.TrimSpace(checked))
		if !strings.Contains(lowerChecked, "no open gates") &&
			!strings.Contains(lowerChecked, "resolved") {
			t.Errorf("[%s] gate check output lacks a clean result:\n%s", r.name, checked)
		}

		// A bead gate exercises the transactional watcher path: once the
		// awaited bead closes, gate check must close the gate and report one
		// resolution (rather than merely observing an already-closed gate).
		r.mustRun(t, bd, "create", "Awaited bead", "--id", "mg-awaited")
		beadCreated := r.mustRun(t, bd, "gate", "create", "--type=bead", "--blocks", "mg-target", "--await-id", "mg-awaited", "--reason", "awaited")
		beadGateID := parseCreatedGateID(t, beadCreated)
		r.mustRun(t, bd, "close", "mg-awaited", "--reason", "awaited bead complete")
		beadChecked := r.mustRun(t, bd, "gate", "check", "--type", "bead")
		if !strings.Contains(strings.ToLower(beadChecked), "1 resolved") {
			t.Errorf("[%s] bead gate check output = %q, want one resolved gate", r.name, beadChecked)
		}
		beadShow := r.mustRun(t, bd, "gate", "show", beadGateID)
		if !strings.Contains(beadShow, "Status: closed") {
			t.Errorf("[%s] bead gate %s remains open after check:\n%s", r.name, beadGateID, beadShow)
		}
	}
}
