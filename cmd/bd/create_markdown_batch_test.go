package main

// End-to-end cover for `bd create --file` on the DIRECT route, which is where
// this command's behaviour changed when it moved onto issueops.BatchCreator.
// They run the real bd binary against an isolated embedded-Dolt workspace and
// reuse create_deps_atomic_test.go's hermetic environment and output helpers.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMarkdownPlan writes a plan file and returns its path.
func writeMarkdownPlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write markdown plan: %v", err)
	}
	return path
}

func TestCreateFromMarkdownFile(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	t.Run("creates_every_template_in_the_file", func(t *testing.T) {
		plan := writeMarkdownPlan(t, dir, "plan.md", `## First from file

Opening body.

### Priority
1

### Type
bug

### Labels
api, batch

## Second from file

### Description
Second body.
`)
		out := runCreateDepsBD(t, bd, dir, "create", "--file", plan)
		if !strings.Contains(out, "Created 2 issues from") {
			t.Errorf("create --file output = %q, want the two-issue summary", out)
		}
		// The second template names no priority, so it asks the role for the
		// default rather than spelling 2; a role that missed the ask stores P0.
		for _, want := range []string{"First from file [P1, bug]", "Second from file [P2, task]"} {
			if !strings.Contains(out, want) {
				t.Errorf("create --file output = %q, want %q", out, want)
			}
		}
		titles := createDepsIssueTitles(t, bd, dir)
		for _, title := range []string{"First from file", "Second from file"} {
			if !titles[title] {
				t.Errorf("issue %q was not created", title)
			}
		}
	})

	// The direct route used to hand the whole slice to the store's batch create
	// with no create-only guard and no per-item content validation. A file the
	// workspace refuses now refuses WHOLE, so a caller can fix the file and
	// re-run it without working out which half landed.
	t.Run("a_refused_template_leaves_nothing_behind", func(t *testing.T) {
		plan := writeMarkdownPlan(t, dir, "half-bad.md", `## Lands before the refusal

### Description
Fine.

## Names a dependency that does not exist

### Dependencies
test-nosuchissue
`)
		out, err := runCreateDepsBDRaw(bd, dir, "create", "--file", plan)
		if err == nil {
			t.Fatalf("create --file with an unresolvable dependency exited 0; output:\n%s", out)
		}
		titles := createDepsIssueTitles(t, bd, dir)
		if titles["Lands before the refusal"] {
			t.Error(`issue "Lands before the refusal" persisted despite the file being refused: ` +
				"a --file create is all or nothing")
		}
		if titles["Names a dependency that does not exist"] {
			t.Error("the refusing template itself persisted")
		}
	})

	// --ephemeral is not in singleIssueOnlyFlags, so this route has always
	// ACCEPTED it while building the issues without reading it: a caller who
	// asked for scratch work got durable rows and no warning.
	t.Run("ephemeral_is_honoured_rather_than_accepted_and_ignored", func(t *testing.T) {
		plan := writeMarkdownPlan(t, dir, "scratch.md", `## Scratch from file

### Description
Ephemeral body.
`)
		out := runCreateDepsBD(t, bd, dir, "create", "--file", plan, "--ephemeral", "--json")
		if !strings.Contains(out, `"ephemeral": true`) && !strings.Contains(out, `"ephemeral":true`) {
			t.Errorf("create --file --ephemeral --json does not report an ephemeral row:\n%s", out)
		}
		// And the durable listing does not carry it, which is the half a
		// caller asking for scratch work actually depends on.
		if createDepsIssueTitles(t, bd, dir)["Scratch from file"] {
			t.Error(`"Scratch from file" is in the durable listing; --file --ephemeral must create a wisp`)
		}
	})

	// The edges the file declares are written with the issues, in the same
	// transaction — including an edge onto an issue created EARLIER in the file.
	t.Run("writes_the_edges_the_file_declares", func(t *testing.T) {
		blocker := strings.TrimSpace(runCreateDepsBD(t, bd, dir, "create", "file edge blocker", "--silent"))
		if blocker == "" {
			t.Fatal("blocker create returned empty ID")
		}
		plan := writeMarkdownPlan(t, dir, "edges.md", `## Blocked by an existing issue

### Dependencies
`+blocker+`
`)
		runCreateDepsBD(t, bd, dir, "create", "--file", plan)
		// --direction=up is what the blocker BLOCKS, which is where the edge
		// the file declared shows up.
		out := runCreateDepsBD(t, bd, dir, "dep", "tree", blocker, "--direction=up", "--json")
		if !strings.Contains(out, "Blocked by an existing issue") {
			t.Errorf("the declared edge was not written; dependents of %s:\n%s", blocker, out)
		}
	})

	// GH#7508: depends-on: and blocked-by: are --deps aliases for a blocking
	// edge. The file used to store them as literal, non-gating types, so the
	// issue stayed in bd ready while bd show printed the edge.
	t.Run("deps_alias_spellings_gate_like_the_deps_flag", func(t *testing.T) {
		target := createMarkdownDepTarget(t, bd, dir, "alias spelling target 7508")
		plan := writeMarkdownPlan(t, dir, "aliases.md", `## Alias case bare id 7508

### Deps
`+target+`

## Alias case depends-on 7508

### Deps
depends-on:`+target+`

## Alias case blocked-by 7508

### Deps
blocked-by:`+target+`
`)
		runCreateDepsBD(t, bd, dir, "create", "--file", plan)
		blocked := runCreateDepsBD(t, bd, dir, "blocked", "--json")
		ready := runCreateDepsBD(t, bd, dir, "ready", "--json", "--limit", "0")
		for _, title := range []string{"Alias case bare id 7508", "Alias case depends-on 7508", "Alias case blocked-by 7508"} {
			if !strings.Contains(blocked, title) {
				t.Errorf("%q is not in bd blocked; its edge on %s does not gate:\n%s", title, target, blocked)
			}
			if strings.Contains(ready, title) {
				t.Errorf("%q is in bd ready although it depends on open %s", title, target)
			}
		}
	})

	// GH#7508: --deps blocks:<id> makes <id> depend on the new issue. The file
	// used to write the opposite edge from the same string.
	t.Run("blocks_spelling_reverses_like_the_deps_flag", func(t *testing.T) {
		target := createMarkdownDepTarget(t, bd, dir, "blocks spelling target 7508")
		plan := writeMarkdownPlan(t, dir, "blocks.md", `## Blocks spelling blocker 7508

### Deps
blocks:`+target+`
`)
		runCreateDepsBD(t, bd, dir, "create", "--file", plan)
		blocked := runCreateDepsBD(t, bd, dir, "blocked", "--json")
		ready := runCreateDepsBD(t, bd, dir, "ready", "--json", "--limit", "0")
		if !strings.Contains(blocked, "blocks spelling target 7508") {
			t.Errorf("target %s is not in bd blocked; blocks: did not make it depend on the new issue:\n%s", target, blocked)
		}
		if strings.Contains(blocked, "Blocks spelling blocker 7508") {
			t.Errorf("the blocks: template is in bd blocked; its edge points the wrong way:\n%s", blocked)
		}
		if !strings.Contains(ready, "Blocks spelling blocker 7508") {
			t.Errorf("the blocks: template is not in bd ready:\n%s", ready)
		}
	})

	// GH#7508: a type --deps refuses (a misspelling or a custom type) used to
	// pass the file's own length check and land as a non-gating edge. It now
	// refuses the whole file, like any other refused template.
	t.Run("a_dep_type_deps_refuses_refuses_the_whole_file", func(t *testing.T) {
		target := createMarkdownDepTarget(t, bd, dir, "refused type target 7508")
		for _, depType := range []string{"blokcs", "implements"} {
			good := "Lands beside the " + depType + " refusal 7508"
			bad := "Names the " + depType + " type 7508"
			plan := writeMarkdownPlan(t, dir, depType+".md", `## `+good+`

### Deps
`+target+`

## `+bad+`

### Deps
`+depType+`:`+target+`
`)
			out, err := runCreateDepsBDRaw(bd, dir, "create", "--file", plan)
			if err == nil {
				t.Fatalf("create --file with %s:%s exited 0; output:\n%s", depType, target, out)
			}
			for _, want := range []string{"unknown dependency type", depType, bad} {
				if !strings.Contains(out, want) {
					t.Errorf("create --file with %s:%s output = %q, want %q", depType, target, out, want)
				}
			}
			titles := createDepsIssueTitles(t, bd, dir)
			for _, title := range []string{good, bad} {
				if titles[title] {
					t.Errorf("issue %q persisted although the file was refused", title)
				}
			}
		}
	})

	// GH#7508: once the aliases mean blocks, one edge spelled three ways is
	// three identical edges, which the create role refuses as duplicates.
	// --deps dedupes them; so does the file.
	t.Run("a_repeated_edge_is_written_once", func(t *testing.T) {
		target := createMarkdownDepTarget(t, bd, dir, "repeated edge target 7508")
		const title = "Repeats one edge three ways 7508"
		plan := writeMarkdownPlan(t, dir, "repeated.md", `## `+title+`

### Deps
depends-on:`+target+`, blocked-by:`+target+`, `+target+`
`)
		runCreateDepsBD(t, bd, dir, "create", "--file", plan)
		out := runCreateDepsBD(t, bd, dir, "dep", "list", target, "--direction=up", "--json")
		var dependents []struct {
			Title          string `json:"title"`
			DependencyType string `json:"dependency_type"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &dependents); err != nil {
			t.Fatalf("parse bd dep list --json: %v\n%s", err, out)
		}
		var edges []string
		for _, d := range dependents {
			if d.Title == title {
				edges = append(edges, d.DependencyType)
			}
		}
		if len(edges) != 1 || edges[0] != "blocks" {
			t.Errorf("edges from %q to %s = %v, want exactly one blocks edge:\n%s", title, target, edges, out)
		}
		if blocked := runCreateDepsBD(t, bd, dir, "blocked", "--json"); !strings.Contains(blocked, title) {
			t.Errorf("%q is not in bd blocked:\n%s", title, blocked)
		}
	})
}

// createMarkdownDepTarget creates a fresh open issue for a file's edges to
// name, so no other subtest's edge can satisfy a ready/blocked check.
func createMarkdownDepTarget(t *testing.T, bd, dir, title string) string {
	t.Helper()
	id := strings.TrimSpace(runCreateDepsBD(t, bd, dir, "create", title, "--silent"))
	if id == "" {
		t.Fatalf("create %q returned empty ID", title)
	}
	return id
}
