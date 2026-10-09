//go:build cgo

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestProxiedServerMoleculeLifecycleAgreesOnEveryRoute drives one molecule
// lifecycle — `bd create --parent`, `bd close --continue`, `bd ready --mol`,
// `bd mol current`, `bd mol progress` and the auto-close after the last step —
// through the real bd binary on the three routes a workspace can take:
//
//   - direct: a server-mode workspace, the CLI on the opened store;
//   - proxied: a --proxied-server workspace, the CLI on the provider;
//   - remote: `bd connect` to a `bd serve` of a third server-mode workspace.
//
// Each step of the lifecycle has ONE library entry (issueops), so every route
// must leave the same rows behind and print the same JSON, timestamps aside.
// The fixture puts a ready step that ANOTHER actor holds first in line, which is
// the case the old direct route got wrong (a status-only claim that took it).
func TestProxiedServerMoleculeLifecycleAgreesOnEveryRoute(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	directProject := newServerModeProject(t, bd, "mr")
	proxiedProject := newSharedProxiedProject(t, bd, "mr")
	servedProject := newServerModeProject(t, bd, "mr")
	sp := startServe(t, bd, servedProject.dir, servedProject.env)
	remoteDir := t.TempDir()
	initGitRepoAt(t, remoteDir)
	remoteEnv := bdEnv(remoteDir)

	routes := []moleculeFrontDoorRunner{
		{name: "direct", dir: directProject.dir, env: func(string) []string { return directProject.env }},
		{name: "proxied", dir: proxiedProject.dir, env: bdProxiedEnv},
		{name: "remote", dir: remoteDir, env: func(string) []string { return remoteEnv }},
	}
	routes[2].mustRun(t, bd, "connect", "http://"+sp.addr, "--json")

	as := func(args ...string) []string { return append([]string{"--actor", "worker"}, args...) }

	// The fixture. Child ids are minted by the create role on every route.
	for _, r := range routes {
		r.mustRun(t, bd, as("create", "Route molecule", "--type", "molecule", "--id", "mr-root")...)
		for i, title := range []string{"first step", "held step", "free step"} {
			out := r.mustRun(t, bd, as("create", title, "--type", "task", "--parent", "mr-root", "--json")...)
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &created); err != nil {
				t.Fatalf("[%s] parse create: %v\n%s", r.name, err, out)
			}
			if want := "mr-root." + string(rune('1'+i)); created.ID != want {
				t.Fatalf("[%s] create --parent minted %q, want %q", r.name, created.ID, want)
			}
		}
		r.mustRun(t, bd, as("update", "mr-root.1", "--status", "in_progress", "--assignee", "worker")...)
		r.mustRun(t, bd, as("update", "mr-root.2", "--assignee", "someone-else")...)
	}

	compare := func(t *testing.T, name string, args ...string) {
		t.Helper()
		var first any
		for i, r := range routes {
			got := dropMoleculeRouteRevisions(normalizedMoleculeJSON(t, r.mustRun(t, bd, as(args...)...)))
			if i == 0 {
				first = got
				continue
			}
			if !reflect.DeepEqual(first, got) {
				t.Errorf("%s diverged: %s answered\n%v\n%s answered\n%v", name, routes[0].name, first, r.name, got)
			}
		}
	}

	compare(t, "mol current", "mol", "current", "mr-root", "--json")
	compare(t, "mol progress", "mol", "progress", "mr-root", "--json")
	compare(t, "ready --mol", "ready", "--mol", "mr-root", "--json")

	// close --continue: the next ready step is mr-root.2, but someone else
	// holds it, so the advance claims mr-root.3 for worker and leaves .2 alone.
	compare(t, "close --continue", "close", "mr-root.1", "--continue", "--json")
	for _, r := range routes {
		assertMoleculeRouteRow(t, bd, r, "mr-root.2", "open", "someone-else")
		assertMoleculeRouteRow(t, bd, r, "mr-root.3", "in_progress", "worker")
		assertMoleculeRouteRow(t, bd, r, "mr-root", "open", "")
	}

	// Closing the last two steps closes the root, in the close's own
	// transaction, on every route.
	compare(t, "close free step", "close", "mr-root.3", "--json")
	compare(t, "close held step", "close", "mr-root.2", "--force", "--json")
	for _, r := range routes {
		assertMoleculeRouteRow(t, bd, r, "mr-root", "closed", "")
		out := r.mustRun(t, bd, "show", "mr-root", "--json")
		if !strings.Contains(out, `"close_reason": "all steps complete"`) && !strings.Contains(out, `"close_reason":"all steps complete"`) {
			t.Errorf("[%s] root close_reason is not the auto-close's:\n%s", r.name, out)
		}
	}
}

func assertMoleculeRouteRow(t *testing.T, bd string, r moleculeFrontDoorRunner, id, status, assignee string) {
	t.Helper()
	out := r.mustRun(t, bd, "show", id, "--json")
	var rows []struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("[%s] parse show %s: %v\n%s", r.name, id, err, out)
	}
	if rows[0].Status != status || rows[0].Assignee != assignee {
		t.Errorf("[%s] %s = %s/%q, want %s/%q", r.name, id, rows[0].Status, rows[0].Assignee, status, assignee)
	}
}

// dropMoleculeRouteRevisions removes the opaque row-version tokens, which each
// database mints on its own, and the lease's wall-clock fields.
func dropMoleculeRouteRevisions(value any) any {
	switch v := value.(type) {
	case []any:
		for i := range v {
			v[i] = dropMoleculeRouteRevisions(v[i])
		}
	case map[string]any:
		// The claim's lease clock is wall time, like the timestamps the shared
		// normalizer already drops.
		for _, key := range []string{"revision", "heartbeat_at", "lease_expires_at"} {
			delete(v, key)
		}
		for key, item := range v {
			v[key] = dropMoleculeRouteRevisions(item)
		}
	}
	return value
}
