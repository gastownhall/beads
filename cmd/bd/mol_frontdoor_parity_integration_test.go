//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

// moleculeFrontDoorRunner is deliberately a thin command runner.  The
// fixture and assertions below must exercise the real bd front door in both
// direct SQL-server and proxied-server workspaces; neither side is allowed to
// call a store implementation directly.
type moleculeFrontDoorRunner struct {
	name string
	dir  string
	env  func(string) []string
}

func (r moleculeFrontDoorRunner) run(t *testing.T, bd string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env(r.dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

func (r moleculeFrontDoorRunner) mustRun(t *testing.T, bd string, args ...string) string {
	t.Helper()
	stdout, stderr, err := r.run(t, bd, args...)
	if err != nil {
		t.Fatalf("[%s] bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			r.name, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// buildMoleculeFrontDoorFixture creates the same explicit-ID graph in each
// workspace.  Explicit IDs make the JSON comparison useful even though the
// two databases have independent Dolt histories and timestamps.
func buildMoleculeFrontDoorFixture(t *testing.T, bd string, r moleculeFrontDoorRunner) {
	t.Helper()
	r.mustRun(t, bd, "create", "Parity molecule", "--type", "epic", "--id", "mf-root")
	for _, issue := range []struct {
		id    string
		title string
	}{
		{id: "mf-done", title: "Done step"},
		{id: "mf-active", title: "Active step"},
		{id: "mf-ready", title: "Ready step"},
	} {
		r.mustRun(t, bd, "create", issue.title, "--type", "task", "--id", issue.id)
		r.mustRun(t, bd, "dep", "add", issue.id, "mf-root", "--type", "parent-child")
	}
	// A closed gate blocks a step in a second molecule. `mol ready --gated`
	// must discover that molecule after the gate closes, not just return an
	// empty result that would hide an unwired ready surface.
	r.mustRun(t, bd, "config", "set", "types.custom", "gate")
	r.mustRun(t, bd, "create", "Ready molecule", "--type", "epic", "--id", "mf-ready-root")
	r.mustRun(t, bd, "create", "Ready molecule step", "--type", "task", "--id", "mf-ready-root-step")
	r.mustRun(t, bd, "create", "Ready molecule gate", "--type", "gate", "--id", "mf-ready-gate")
	r.mustRun(t, bd, "dep", "add", "mf-ready-root-step", "mf-ready-root", "--type", "parent-child")
	r.mustRun(t, bd, "dep", "add", "mf-ready-gate", "mf-ready-root", "--type", "parent-child")
	r.mustRun(t, bd, "dep", "add", "mf-ready-root-step", "mf-ready-gate")
	r.mustRun(t, bd, "close", "mf-ready-gate", "--reason", "ready discovery fixture")
	r.mustRun(t, bd, "close", "mf-done", "--reason", "front-door parity fixture")
	r.mustRun(t, bd, "update", "mf-active", "--status", "in_progress", "--assignee", "worker1")
	// This edge gives mol show/progress/ready a non-trivial dependency graph.
	r.mustRun(t, bd, "dep", "add", "mf-ready", "mf-active")
}

// normalizedMoleculeJSON removes only wall-clock fields.  IDs, statuses,
// dependencies, counts, and metadata remain part of the parity contract.
func normalizedMoleculeJSON(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &value); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, raw)
	}
	return normalizeMoleculeValue(value)
}

func normalizeMoleculeValue(value any) any {
	return normalizeMoleculeValueAt(value, false)
}

func normalizeMoleculeValueAt(value any, preserveTimestamps bool) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizeMoleculeValueAt(item, preserveTimestamps)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if !preserveTimestamps && moleculeTimestampKey(key) {
				continue
			}
			normalized := normalizeMoleculeValueAt(item, preserveTimestamps || key == "metadata")
			if key == "metadata" && emptyMoleculeMetadata(normalized) {
				// Direct SQL serializes an empty metadata object as "{}" while
				// the proxy omits it. Empty metadata carries no semantics, so
				// omit both spellings; non-empty metadata remains compared.
				continue
			}
			out[key] = normalized
		}
		return out
	default:
		return value
	}
}

func moleculeTimestampKey(key string) bool {
	switch key {
	case "created_at", "updated_at", "closed_at", "started_at", "completed_at", "last_activity_at", "timestamp":
		return true
	default:
		return false
	}
}

func emptyMoleculeMetadata(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == "" || strings.TrimSpace(v) == "{}"
	case map[string]any:
		return len(v) == 0
	default:
		return false
	}
}

func TestNormalizeMoleculeMetadataPreservesNonEmpty(t *testing.T) {
	got, ok := normalizeMoleculeValue(map[string]any{
		"updated_at": "wire timestamp",
		"metadata": map[string]any{
			"updated_at": "user metadata",
			"team":       "platform",
		},
	}).(map[string]any)
	if !ok {
		t.Fatalf("normalized value has type %T, want map", got)
	}
	if _, exists := got["updated_at"]; exists {
		t.Error("top-level updated_at should be normalized away")
	}
	metadata, ok := got["metadata"].(map[string]any)
	if !ok || metadata["updated_at"] != "user metadata" || metadata["team"] != "platform" {
		t.Errorf("non-empty metadata was not preserved: %#v", got["metadata"])
	}
}

func TestMoleculeFrontDoorDirectServerProxyParity(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	// Keep the configured issue prefix identical so explicit IDs are accepted
	// by both databases; each helper still allocates an independent database.
	directProject := newServerModeProject(t, bd, "mf")
	proxiedProject := newSharedProxiedProject(t, bd, "mf")
	direct := moleculeFrontDoorRunner{name: "direct-sql", dir: directProject.dir, env: func(string) []string { return directProject.env }}
	proxied := moleculeFrontDoorRunner{name: "proxied", dir: proxiedProject.dir, env: bdProxiedEnv}
	for _, r := range []moleculeFrontDoorRunner{direct, proxied} {
		buildMoleculeFrontDoorFixture(t, bd, r)
	}

	// Read-only molecule surfaces must return the same graph and lifecycle
	// facts.  The JSON normalizer intentionally leaves all semantic fields in
	// place while removing only timestamps generated by each independent DB.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "show", args: []string{"mol", "show", "mf-root", "--json"}},
		{name: "current", args: []string{"mol", "current", "mf-root", "--json"}},
		{name: "progress", args: []string{"mol", "progress", "mf-root", "--json"}},
		{name: "ready", args: []string{"mol", "ready", "--gated", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directRaw := direct.mustRun(t, bd, tc.args...)
			proxiedRaw := proxied.mustRun(t, bd, tc.args...)
			assertMoleculeFrontDoorRead(t, tc.name, directRaw)
			assertMoleculeFrontDoorRead(t, tc.name, proxiedRaw)
			directJSON := normalizedMoleculeJSON(t, directRaw)
			proxiedJSON := normalizedMoleculeJSON(t, proxiedRaw)
			if !reflect.DeepEqual(directJSON, proxiedJSON) {
				t.Errorf("%s JSON diverged between direct SQL and proxy:\ndirect: %v\nproxy:  %v", tc.name, directJSON, proxiedJSON)
			}
		})
	}

	// Last-activity carries an intentionally dynamic timestamp, so compare the
	// stable source and step identity fields explicitly.
	for _, r := range []moleculeFrontDoorRunner{direct, proxied} {
		var got struct {
			MoleculeID   string `json:"molecule_id"`
			Source       string `json:"source"`
			SourceStepID string `json:"source_step_id"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "last-activity", "mf-root", "--json")), &got); err != nil {
			t.Fatalf("[%s] parse mol last-activity: %v", r.name, err)
		}
		if got.MoleculeID != "mf-root" || got.Source != "step_updated" || got.SourceStepID != "mf-active" {
			t.Errorf("[%s] last-activity = %+v, want root/step_updated/mf-active", r.name, got)
		}
	}

	// The graph and lifecycle mutations above must be visible through the
	// ordinary issue reader too, including every explicit ID and status.
	directListRaw := direct.mustRun(t, bd, "list", "--all", "--json")
	proxiedListRaw := proxied.mustRun(t, bd, "list", "--all", "--json")
	assertMoleculeFrontDoorList(t, "direct-sql", directListRaw)
	assertMoleculeFrontDoorList(t, "proxied", proxiedListRaw)
	if !reflect.DeepEqual(normalizedMoleculeIssueSet(t, directListRaw), normalizedMoleculeIssueSet(t, proxiedListRaw)) {
		t.Errorf("list --all --json diverged between direct SQL and proxy:\ndirect: %v\nproxy:  %v",
			normalizedMoleculeIssueSet(t, directListRaw), normalizedMoleculeIssueSet(t, proxiedListRaw))
	}
}

func normalizedMoleculeIssueSet(t *testing.T, raw string) map[string]any {
	t.Helper()
	value := normalizedMoleculeJSON(t, raw)
	rows, ok := value.([]any)
	if !ok {
		t.Fatalf("normalized list has type %T, want array", value)
	}
	set := make(map[string]any, len(rows))
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("normalized list row has type %T, want object", row)
		}
		id, ok := obj["id"].(string)
		if !ok || id == "" {
			t.Fatalf("normalized list row has no string id: %#v", obj)
		}
		set[id] = obj
	}
	return set
}

func assertMoleculeFrontDoorList(t *testing.T, mode, raw string) {
	t.Helper()
	var rows []struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rows); err != nil {
		t.Fatalf("[%s] parse list --all --json: %v\n%s", mode, err, raw)
	}
	want := map[string]struct {
		status   string
		assignee string
	}{
		"mf-root":            {status: "open"},
		"mf-active":          {status: "in_progress", assignee: "worker1"},
		"mf-done":            {status: "closed"},
		"mf-ready":           {status: "open"},
		"mf-ready-root":      {status: "open"},
		"mf-ready-root-step": {status: "open"},
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if expected, ok := want[row.ID]; ok {
			seen[row.ID] = true
			if row.Status != expected.status || row.Assignee != expected.assignee {
				t.Errorf("[%s] %s status/assignee = %s/%q, want %s/%q", mode, row.ID, row.Status, row.Assignee, expected.status, expected.assignee)
			}
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("[%s] list --all --json omitted %s", mode, id)
		}
	}
}

func assertMoleculeFrontDoorRead(t *testing.T, name, raw string) {
	t.Helper()
	switch name {
	case "show":
		var got struct {
			Root struct {
				ID string `json:"id"`
			} `json:"root"`
			Issues       []struct{} `json:"issues"`
			Dependencies []struct{} `json:"dependencies"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("parse mol show: %v", err)
		}
		if got.Root.ID != "mf-root" || len(got.Issues) != 4 || len(got.Dependencies) != 4 {
			t.Errorf("mol show = %+v, want mf-root with four issues and four dependencies", got)
		}
	case "current":
		var got []struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("parse mol current: %v", err)
		}
		if len(got) != 1 || got[0].Total != 3 || got[0].Completed != 1 {
			t.Errorf("mol current = %+v, want one molecule with 1/3 complete", got)
		}
	case "progress":
		var got struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("parse mol progress: %v", err)
		}
		if got.Total != 3 || got.Completed != 1 {
			t.Errorf("mol progress = %+v, want 1/3 complete", got)
		}
	case "ready":
		var got struct {
			Molecules []struct {
				MoleculeID string `json:"molecule_id"`
			} `json:"molecules"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("parse mol ready: %v", err)
		}
		if len(got.Molecules) != 1 || got.Molecules[0].MoleculeID != "mf-ready-root" {
			t.Errorf("mol ready --gated = %+v, want mf-ready-root", got)
		}
	}
}

func writeMoleculeFormulaFixture(t *testing.T, r moleculeFrontDoorRunner, f *formula.Formula) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal formula %s: %v", f.Formula, err)
	}
	dir := filepath.Join(r.dir, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.Formula+formula.FormulaExt), data, 0o644); err != nil {
		t.Fatalf("write formula fixture %s: %v", f.Formula, err)
	}
}

func TestMoleculeFrontDoorWriteParity(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	directProject := newServerModeProject(t, bd, "mw")
	proxiedProject := newSharedProxiedProject(t, bd, "mw")
	direct := moleculeFrontDoorRunner{name: "direct-sql", dir: directProject.dir, env: func(string) []string { return directProject.env }}
	proxied := moleculeFrontDoorRunner{name: "proxied", dir: proxiedProject.dir, env: bdProxiedEnv}
	modes := []moleculeFrontDoorRunner{direct, proxied}

	// mol bond is a transactional graph write. Explicit IDs let us compare
	// the result and the resulting dependency through the two front doors.
	for _, r := range modes {
		r.mustRun(t, bd, "create", "Bond A", "--type", "epic", "--id", "mw-a")
		r.mustRun(t, bd, "create", "Bond B", "--type", "epic", "--id", "mw-b")
	}
	for _, r := range modes {
		var got struct {
			ResultID   string `json:"result_id"`
			ResultType string `json:"result_type"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "bond", "mw-a", "mw-b", "--type", "sequential", "--json")), &got); err != nil {
			t.Fatalf("[%s] parse mol bond: %v", r.name, err)
		}
		if got.ResultID != "mw-a" || got.ResultType != "compound_molecule" {
			t.Errorf("[%s] mol bond result = %+v, want mw-a/compound_molecule", r.name, got)
		}
	}
	for _, r := range modes {
		show := normalizedMoleculeJSON(t, r.mustRun(t, bd, "mol", "show", "mw-a", "--json"))
		if !strings.Contains(strings.TrimSpace(r.mustRun(t, bd, "dep", "list", "mw-b", "--json")), "mw-a") {
			t.Errorf("[%s] bond dependency does not point at mw-a", r.name)
		}
		if show == nil {
			t.Errorf("[%s] bonded molecule show returned nil", r.name)
		}
	}

	// Formula instantiation is a second positive write family. Compare the
	// stable count/phase fields for persistent pours and ephemeral wisps; IDs
	// generated from independent transactions are intentionally not compared.
	f := twoStepFormula("frontdoor-write", "Write parity step")
	for _, r := range modes {
		writeMoleculeFormulaFixture(t, r, f)
	}
	var pourCreated, wispCreated int
	for _, r := range modes {
		var pour struct {
			Created int    `json:"created"`
			Phase   string `json:"phase"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "pour", f.Formula, "--json")), &pour); err != nil {
			t.Fatalf("[%s] parse mol pour: %v", r.name, err)
		}
		if pour.Created != 2 || pour.Phase != "liquid" {
			t.Errorf("[%s] mol pour = %+v, want created=2 phase=liquid", r.name, pour)
		}
		if pourCreated == 0 {
			pourCreated = pour.Created
		} else if pour.Created != pourCreated {
			t.Errorf("[%s] mol pour created=%d, want %d", r.name, pour.Created, pourCreated)
		}

		var wisp struct {
			Created int    `json:"created"`
			Phase   string `json:"phase"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "wisp", "create", f.Formula, "--json")), &wisp); err != nil {
			t.Fatalf("[%s] parse mol wisp create: %v", r.name, err)
		}
		if wisp.Created != 2 || wisp.Phase != "vapor" {
			t.Errorf("[%s] mol wisp create = %+v, want created=2 phase=vapor", r.name, wisp)
		}
		if wispCreated == 0 {
			wispCreated = wisp.Created
		} else if wisp.Created != wispCreated {
			t.Errorf("[%s] mol wisp created=%d, want %d", r.name, wisp.Created, wispCreated)
		}
	}

	// Squash and burn exercise cleanup and transaction boundaries. Each mode
	// gets independent IDs because these operations are intentionally
	// destructive and should not share state across assertions.
	type squashSummary struct {
		squashed int
		deleted  int
		wisp     bool
	}
	var squashResults []squashSummary
	var burnResults []int
	for _, r := range modes {
		r.mustRun(t, bd, "create", "Squash root", "--type", "epic", "--id", "mw-squash", "--ephemeral")
		r.mustRun(t, bd, "create", "Squash child", "--type", "task", "--id", "mw-squash-child", "--ephemeral")
		r.mustRun(t, bd, "dep", "add", "mw-squash-child", "mw-squash", "--type", "parent-child")
		var squash struct {
			SquashedCount int  `json:"squashed_count"`
			DeletedCount  int  `json:"deleted_count"`
			WispSquash    bool `json:"wisp_squash"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "squash", "mw-squash", "--json")), &squash); err != nil {
			t.Fatalf("[%s] parse mol squash: %v", r.name, err)
		}
		if squash.SquashedCount != 1 || squash.DeletedCount != 1 || !squash.WispSquash {
			t.Errorf("[%s] mol squash = %+v, want 1/1/wisp", r.name, squash)
		}
		squashResults = append(squashResults, squashSummary{squash.SquashedCount, squash.DeletedCount, squash.WispSquash})
		r.mustRun(t, bd, "create", "Burn root", "--type", "epic", "--id", "mw-burn")
		r.mustRun(t, bd, "create", "Burn child", "--type", "task", "--id", "mw-burn-child")
		r.mustRun(t, bd, "dep", "add", "mw-burn-child", "mw-burn", "--type", "parent-child")
		var burn struct {
			TotalDeleted int `json:"total_deleted"`
			DeletedCount int `json:"deleted_count"`
		}
		if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "burn", "mw-burn", "--force", "--json")), &burn); err != nil {
			t.Fatalf("[%s] parse mol burn: %v", r.name, err)
		}
		deleted := burn.TotalDeleted
		if deleted == 0 {
			// Direct mode emits the single-molecule result shape while the
			// proxied route wraps it in BatchBurnResult.
			deleted = burn.DeletedCount
		}
		if deleted != 2 {
			t.Errorf("[%s] mol burn deleted=%d, want 2", r.name, deleted)
		}
		burnResults = append(burnResults, deleted)
	}
	if len(squashResults) == 2 && squashResults[0] != squashResults[1] {
		t.Errorf("mol squash direct/proxy results diverged: direct=%+v proxy=%+v", squashResults[0], squashResults[1])
	}
	if len(burnResults) == 2 && burnResults[0] != burnResults[1] {
		t.Errorf("mol burn direct/proxy results diverged: direct=%d proxy=%d", burnResults[0], burnResults[1])
	}
}
