package main

// Regression tests for GH#5585: `bd link` and `bd batch`'s `dep.add` op built
// a types.DependencyType directly from the raw --type/args value and only
// called IsValid(), skipping the canonicalDependencyType/validateDependencyType
// pair GH#5069/#5116 introduced for `bd dep add --type` and `bd create --deps`.
// An alias like "blocked-by" was stored verbatim instead of normalizing to the
// canonical "blocks" type, so the edge never gated `bd ready`/`bd blocked`.
// batch dep.add also accepted a typo'd/made-up type outright; bd link keeps
// accepting well-formed custom types by design (see linkDependencyType).
//
// These tests run the real bd binary, built with the gms_pure_go embedded-Dolt
// engine (see buildBDForInitTests), so they need no cgo and no external Dolt
// server. They reuse the createDepsTestEnv/runCreateDepsBD family of helpers
// from create_deps_atomic_test.go (same package, no build tag).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkBatchDepType returns the stored type of the dependency from `from` to
// `to`, as reported by `bd dep list <from> --json`. That command's --json
// shape is the target issue decorated with a "dependency_type" field (see
// printDepListEdges), not the raw types.Dependency row.
func linkBatchDepType(t *testing.T, bd, dir, from, to string) string {
	t.Helper()
	out := runCreateDepsBD(t, bd, dir, "dep", "list", from, "--json")
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("dep list %s --json: no JSON array in output:\n%s", from, out)
	}
	var deps []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &deps); err != nil {
		t.Fatalf("parse dep list --json: %v\n%s", err, out)
	}
	for _, d := range deps {
		if d.ID == to {
			return d.DependencyType
		}
	}
	t.Fatalf("dep list %s --json: no edge to %s found in %+v", from, to, deps)
	return ""
}

// linkBatchIDSet runs a JSON issue listing (`bd ready --json`, `bd blocked
// --json`) and returns the set of issue ids it names.
func linkBatchIDSet(t *testing.T, bd, dir string, args ...string) map[string]bool {
	t.Helper()
	out := runCreateDepsBD(t, bd, dir, args...)
	set := make(map[string]bool)
	start := strings.Index(out, "[")
	if start < 0 {
		// An empty listing may print a non-JSON notice.
		return set
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &rows); err != nil {
		t.Fatalf("parse bd %v: %v\n%s", args, err, out)
	}
	for _, r := range rows {
		set[r.ID] = true
	}
	return set
}

// linkBatchInit initializes a fresh embedded workspace in a temp dir.
func linkBatchInit(t *testing.T) (bd, dir string) {
	t.Helper()
	bd = buildBDForInitTests(t)
	dir = t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	return bd, dir
}

// TestCLI_Link_TypeBlockedByAliasGates is the `bd link` counterpart to
// TestCLI_DepAdd_TypeBlockedByAliasGates (GH#5069/#5116): `bd link` is
// documented as `dep add` shorthand and must normalize the same aliases,
// store and report the canonical blocks type, and gate ready work with it.
func TestCLI_Link_TypeBlockedByAliasGates(t *testing.T) {
	bd, dir := linkBatchInit(t)

	for _, alias := range []string{"blocked-by", "depends-on"} {
		blocker := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Blocker "+alias, "-p", "1", "--json"))
		blocked := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Blocked "+alias, "-p", "1", "--json"))

		// blocked depends on (is blocked by) blocker, via the alias rather
		// than the default "blocks" type.
		out := runCreateDepsBD(t, bd, dir, "link", blocked, blocker, "--type", alias, "--json")
		var res struct {
			IssueID     string `json:"issue_id"`
			DependsOnID string `json:"depends_on_id"`
			Type        string `json:"type"`
		}
		if start := strings.Index(out, "{"); start < 0 || json.Unmarshal([]byte(out[start:]), &res) != nil {
			t.Fatalf("bd link --type %s --json: unparseable output:\n%s", alias, out)
		}
		if res.Type != "blocks" || res.IssueID != blocked || res.DependsOnID != blocker {
			t.Errorf("bd link --type %s --json = %+v, want %s depends on %s (blocks)", alias, res, blocked, blocker)
		}

		if gotType := linkBatchDepType(t, bd, dir, blocked, blocker); gotType != "blocks" {
			t.Errorf("bd link --type %s stored type %q, want canonical \"blocks\"", alias, gotType)
		}
		if linkBatchIDSet(t, bd, dir, "ready", "--limit", "0", "--json")[blocked] {
			t.Errorf("%s is ready after link --type %s while %s is open", blocked, alias, blocker)
		}
		if !linkBatchIDSet(t, bd, dir, "blocked", "--json")[blocked] {
			t.Errorf("%s is not blocked after link --type %s while %s is open", blocked, alias, blocker)
		}

		textBlocker := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Text Blocker "+alias, "-p", "1", "--json"))
		textBlocked := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Link Text Blocked "+alias, "-p", "1", "--json"))
		text := runCreateDepsBD(t, bd, dir, "link", textBlocked, textBlocker, "--type", alias)
		if !strings.Contains(text, "Linked") || !strings.Contains(text, "(blocks)") || strings.Contains(text, "("+alias+")") {
			t.Errorf("bd link --type %s output = %q, want the stored (blocks) type", alias, text)
		}
	}
}

// TestCLI_Link_CustomTypeKeptAndDoesNotGate pins the other half of bd link's
// type contract (GH#5585): a custom type outside the built-ins is stored and
// reported exactly as given, and that edge alone never takes its source out
// of ready work.
func TestCLI_Link_CustomTypeKeptAndDoesNotGate(t *testing.T) {
	bd, dir := linkBatchInit(t)

	from := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Custom From", "-p", "1", "--json"))
	to := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Custom To", "-p", "1", "--json"))

	out := runCreateDepsBD(t, bd, dir, "link", from, to, "--type", "owned-by", "--json")
	var res struct {
		Type string `json:"type"`
	}
	if start := strings.Index(out, "{"); start < 0 || json.Unmarshal([]byte(out[start:]), &res) != nil {
		t.Fatalf("bd link --type owned-by --json: unparseable output:\n%s", out)
	}
	if res.Type != "owned-by" {
		t.Errorf("bd link --type owned-by --json reports type %q, want owned-by", res.Type)
	}
	if gotType := linkBatchDepType(t, bd, dir, from, to); gotType != "owned-by" {
		t.Errorf("bd link --type owned-by stored type %q, want owned-by unchanged", gotType)
	}
	if !linkBatchIDSet(t, bd, dir, "ready", "--limit", "0", "--json")[from] {
		t.Errorf("%s left ready work after an owned-by link alone; a custom type must not gate", from)
	}

	textFrom := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Custom Text From", "-p", "1", "--json"))
	textTo := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Custom Text To", "-p", "1", "--json"))
	if text := runCreateDepsBD(t, bd, dir, "link", textFrom, textTo, "--type", "owned-by"); !strings.Contains(text, "(owned-by)") {
		t.Errorf("bd link --type owned-by output = %q, want the stored (owned-by) type", text)
	}
}

// TestCLI_Batch_DepAdd_TypeBlockedByAliasGates is the `bd batch` dep.add
// counterpart: the op built a types.DependencyType directly from the raw arg
// and only checked IsValid(), so "blocked-by" was stored verbatim instead of
// normalizing to "blocks".
func TestCLI_Batch_DepAdd_TypeBlockedByAliasGates(t *testing.T) {
	bd, dir := linkBatchInit(t)

	for _, alias := range []string{"blocked-by", "depends-on"} {
		blocker := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch Blocker "+alias, "-p", "1", "--json"))
		blocked := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch Blocked "+alias, "-p", "1", "--json"))

		script := "dep add " + blocked + " " + blocker + " " + alias + "\n"
		scriptFile := filepath.Join(dir, "batch-"+alias+".txt")
		if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
			t.Fatalf("write batch script: %v", err)
		}

		out := runCreateDepsBD(t, bd, dir, "batch", "-f", scriptFile)
		if strings.Contains(strings.ToLower(out), "error") {
			t.Fatalf("bd batch dep.add %s reported an error: %s", alias, out)
		}

		if gotType := linkBatchDepType(t, bd, dir, blocked, blocker); gotType != "blocks" {
			t.Errorf("bd batch dep.add %s stored type %q, want canonical \"blocks\"", alias, gotType)
		}
		if linkBatchIDSet(t, bd, dir, "ready", "--limit", "0", "--json")[blocked] {
			t.Errorf("%s is ready after batch dep.add %s while %s is open", blocked, alias, blocker)
		}
		if !linkBatchIDSet(t, bd, dir, "blocked", "--json")[blocked] {
			t.Errorf("%s is not blocked after batch dep.add %s while %s is open", blocked, alias, blocker)
		}
	}
}

// TestCLI_Batch_DepAdd_UnknownTypeRejected mirrors `bd dep add`'s unknown-type
// rejection for the batch dep.add op: a typo or a custom type on line 2 fails
// the batch with the line number, the type and the accepted vocabulary, and
// line 1's write is rolled back with it.
func TestCLI_Batch_DepAdd_UnknownTypeRejected(t *testing.T) {
	bd, dir := linkBatchInit(t)

	for _, bad := range []string{"blcoks", "owned-by"} {
		a := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch A "+bad, "-p", "1", "--json"))
		b := createDepsExtractID(t, runCreateDepsBD(t, bd, dir, "create", "Batch B "+bad, "-p", "1", "--json"))

		script := "update " + a + " priority=3\ndep add " + a + " " + b + " " + bad + "\n"
		scriptFile := filepath.Join(dir, "batch-"+bad+".txt")
		if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
			t.Fatalf("write batch script: %v", err)
		}

		out, err := runCreateDepsBDRaw(bd, dir, "batch", "-f", scriptFile)
		if err == nil {
			t.Errorf("bd batch dep.add %s exited 0; output:\n%s", bad, out)
		}
		for _, want := range []string{"line 2 (dep add ", `unknown dependency type "` + bad + `"`, "valid types: " + createDepsAcceptedTypeList()} {
			if !strings.Contains(out, want) {
				t.Errorf("bd batch dep.add %s output does not contain %q:\n%s", bad, want, out)
			}
		}

		show := runCreateDepsBD(t, bd, dir, "show", a, "--json")
		var issue struct {
			ID       string `json:"id"`
			Priority int    `json:"priority"`
		}
		start := strings.Index(show, "{")
		if start < 0 || json.NewDecoder(strings.NewReader(show[start:])).Decode(&issue) != nil || issue.ID != a {
			t.Fatalf("bd show %s --json: unparseable output:\n%s", a, show)
		}
		if issue.Priority != 1 {
			t.Errorf("%s priority = %d after the refused batch, want 1: line 1 must roll back", a, issue.Priority)
		}
		if deps := runCreateDepsBD(t, bd, dir, "dep", "list", a, "--json"); strings.Contains(deps, b) {
			t.Errorf("the refused batch left an edge %s -> %s: %s", a, b, deps)
		}
	}
}
