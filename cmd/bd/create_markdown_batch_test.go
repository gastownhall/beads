package main

// End-to-end cover for `bd create --file` on the DIRECT route, which is where
// this command's behaviour changed when it moved onto issueops.BatchCreator.
// They run the real bd binary against an isolated embedded-Dolt workspace and
// reuse create_deps_atomic_test.go's hermetic environment and output helpers.

import (
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
}
