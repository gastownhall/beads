//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestEmbeddedShowUnresolvableDeps drives the PRODUCTION path for be-lpi:
// `bd dep add` writing a real edge, then `bd show` reading it back. The unit
// coverage in internal/workapi constructs the count/row skew directly, which
// proves the arithmetic and says nothing about whether any real command can
// produce that state — this does the opposite, and the two together are the
// claim.
//
// The stored shape being pinned is the one the town's convoy beads carry:
// depends_on_external holds a target this database has no row for, so every
// listing path drops it (issueops.GetDependenciesWithMetadataInTx) while every
// count keeps it. Before the fix that left `dependency_count: 1` beside
// `dependencies: null` with nothing saying why, and `bd show` in text mode
// rendered no dependency section at all.
func TestEmbeddedShowUnresolvableDeps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ud")

	anchor := bdCreate(t, bd, dir, "Anchor with an external edge", "--type", "task")
	local := bdCreate(t, bd, dir, "Fully local blocker", "--type", "task")

	// Both target shapes IsExternalDepTarget classifies as external: an
	// explicit `external:` reference, and a bare id whose prefix names
	// another repository. They land in the same column and fail the same
	// way, so pinning only one would leave the commoner of the two —
	// `bd dep add ud-x liveop-y` — uncovered.
	for _, tc := range []struct {
		name    string
		target  string
		depTyp  string
		heading string
		label   string
	}{
		{"external_reference", "external:liveop:liveop-kmf", "tracks", "TRACKS", "external"},
		{"cross_repo_prefix", "liveop-kmf", "blocks", "DEPENDS ON", "cross-repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := bdCreate(t, bd, dir, "Subject "+tc.name, "--type", "task")
			bdDep(t, bd, dir, "add", subject.ID, tc.target, "--type", tc.depTyp)

			details := bdShowDetails(t, bd, dir, subject.ID)
			depCount, ok := details["dependency_count"].(float64)
			if !ok || depCount != 1 {
				t.Fatalf("dependency_count = %v, want 1 — the edge must still be counted",
					details["dependency_count"])
			}
			if deps, present := details["dependencies"]; present && deps != nil {
				if list, isList := deps.([]interface{}); isList && len(list) != 0 {
					t.Fatalf("dependencies = %v, want empty: the target has no row in this database", deps)
				}
			}
			unresolvable, ok := details["unresolvable_dependencies"].(float64)
			if !ok || unresolvable != 1 {
				t.Fatalf("unresolvable_dependencies = %v, want 1 — a count the caller cannot enumerate must say so (be-lpi)",
					details["unresolvable_dependencies"])
			}

			// #5272: the raw edge is named, not just counted, and the
			// hydrated array still holds only local issues.
			edges, _ := details["unresolvable_dependency_edges"].([]interface{})
			if len(edges) != 1 {
				t.Fatalf("unresolvable_dependency_edges = %v, want the one raw edge", details["unresolvable_dependency_edges"])
			}
			edge, _ := edges[0].(map[string]interface{})
			if edge["depends_on_id"] != tc.target || edge["type"] != tc.depTyp {
				t.Errorf("unresolvable_dependency_edges[0] = %v, want %s/%s", edge, tc.target, tc.depTyp)
			}

			// Text mode: the edge is printed by id in its relationship's
			// section, with no status, title or priority, and because it
			// is shown the stderr notice has nothing left to report.
			stdout, stderr := runShowSplit(t, bd, dir, subject.ID)
			want := tc.heading + "\n  →   " + tc.target + " (" + tc.label + ")\n"
			if !strings.Contains(stdout, want) {
				t.Errorf("bd show stdout does not name the edge as %q:\n%s", want, stdout)
			}
			if strings.Contains(stderr, "no row in this database") {
				t.Errorf("bd show warned about an edge it printed:\nstderr:\n%s", stderr)
			}
		})
	}

	// #5272 MIXED: local and unresolved targets on one issue. The local
	// issue keeps the hydrated array and prints exactly the line it prints
	// with no unresolved neighbours; the unresolved edges appear only in the
	// new member and as id lines after the local ones.
	t.Run("mixed_local_and_unresolved", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Mixed local blocker", "--type", "task")
		control := bdCreate(t, bd, dir, "Local-only control", "--type", "task")
		bdDep(t, bd, dir, "add", control.ID, blocker.ID, "--type", "blocks")
		mixed := bdCreate(t, bd, dir, "Mixed subject", "--type", "task")
		bdDep(t, bd, dir, "add", mixed.ID, blocker.ID, "--type", "blocks")
		bdDep(t, bd, dir, "add", mixed.ID, "liveop-mix", "--type", "blocks")
		bdDep(t, bd, dir, "add", mixed.ID, "external:liveop:liveop-mix", "--type", "tracks")

		details := bdShowDetails(t, bd, dir, mixed.ID)
		deps, _ := details["dependencies"].([]interface{})
		if len(deps) != 1 {
			t.Fatalf("dependencies = %v, want only the local blocker", details["dependencies"])
		}
		if dep, _ := deps[0].(map[string]interface{}); dep["id"] != blocker.ID {
			t.Errorf("dependencies[0].id = %v, want %s", dep["id"], blocker.ID)
		}
		if n, _ := details["dependency_count"].(float64); n != 3 {
			t.Errorf("dependency_count = %v, want 3", details["dependency_count"])
		}
		if n, _ := details["unresolvable_dependencies"].(float64); n != 2 {
			t.Errorf("unresolvable_dependencies = %v, want 2", details["unresolvable_dependencies"])
		}
		edges, _ := details["unresolvable_dependency_edges"].([]interface{})
		var got []string
		for _, e := range edges {
			edge, _ := e.(map[string]interface{})
			got = append(got, fmt.Sprintf("%v/%v", edge["depends_on_id"], edge["type"]))
		}
		if want := []string{"external:liveop:liveop-mix/tracks", "liveop-mix/blocks"}; !slices.Equal(got, want) {
			t.Errorf("unresolvable_dependency_edges = %v, want %v", got, want)
		}

		controlOut, _ := runShowSplit(t, bd, dir, control.ID)
		var localLine string
		for _, line := range strings.Split(controlOut, "\n") {
			if strings.Contains(line, blocker.ID) {
				localLine = line
			}
		}
		if localLine == "" {
			t.Fatalf("control render has no line for %s:\n%s", blocker.ID, controlOut)
		}
		stdout, stderr := runShowSplit(t, bd, dir, mixed.ID)
		for _, want := range []string{
			"DEPENDS ON\n" + localLine + "\n  →   liveop-mix (cross-repo)\n",
			"TRACKS\n  →   external:liveop:liveop-mix (external)\n",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("mixed render lacks %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stderr, "no row in this database") {
			t.Errorf("bd show warned about edges it printed:\nstderr:\n%s", stderr)
		}
	})

	// #5272 GRAPH CONSUMERS: unresolved parent-child and blocks edges are
	// named in the display member and nowhere else. The parent, the hydrated
	// dependencies, dep list, readiness and the local epic's progress and
	// close eligibility read exactly what they read before the edges existed.
	t.Run("graph_consumers_unchanged", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Local epic", "--type", "epic")
		child := bdCreate(t, bd, dir, "Child with unresolved graph edges", "--type", "task")
		bdDep(t, bd, dir, "add", child.ID, epic.ID, "--type", "parent-child")

		graph := func() (epicShow, epicStatus, depList string) {
			epicShow, _ = runShowSplit(t, bd, dir, epic.ID)
			epicStatus, _ = bdRunSplit(t, bd, dir, "epic", "status", "--json")
			depList, _ = bdRunSplit(t, bd, dir, "dep", "list", child.ID, "--json")
			return epicShow, epicStatus, depList
		}
		// The ready rows embed their raw dependency records, so readiness
		// is compared by membership, not bytes.
		readyIDs := func() []string {
			out, _ := bdRunSplit(t, bd, dir, "ready", "--json", "--parent", epic.ID, "--limit", "0")
			var rows []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("parse bd ready --json: %v\n%s", err, out)
			}
			var ids []string
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			return ids
		}
		beforeShow, beforeStatus, beforeDeps := graph()
		beforeReady := readyIDs()
		if !slices.Contains(beforeReady, child.ID) {
			t.Fatalf("control: %s is not ready before the unresolved edges: %v", child.ID, beforeReady)
		}

		bdDep(t, bd, dir, "add", child.ID, "liveop-epic", "--type", "parent-child")
		bdDep(t, bd, dir, "add", child.ID, "liveop-gate", "--type", "blocks")

		afterShow, afterStatus, afterDeps := graph()
		if afterReady := readyIDs(); !slices.Equal(afterReady, beforeReady) {
			t.Errorf("bd ready --parent changed with unresolved edges: before %v, after %v", beforeReady, afterReady)
		}
		for _, c := range []struct{ name, before, after string }{
			{"bd show <epic>", beforeShow, afterShow},
			{"bd epic status --json", beforeStatus, afterStatus},
			{"bd dep list --json", beforeDeps, afterDeps},
		} {
			if c.before != c.after {
				t.Errorf("%s changed with unresolved edges:\nbefore:\n%s\nafter:\n%s", c.name, c.before, c.after)
			}
		}
		_, depListStderr := bdRunSplit(t, bd, dir, "dep", "list", child.ID)
		for _, want := range []string{"liveop-epic via parent-child", "liveop-gate via blocks"} {
			if !strings.Contains(depListStderr, want) {
				t.Errorf("bd dep list stderr lacks %q:\n%s", want, depListStderr)
			}
		}

		details := bdShowDetails(t, bd, dir, child.ID)
		if details["parent"] != epic.ID {
			t.Errorf("parent = %v, want the local epic %s", details["parent"], epic.ID)
		}
		if v, present := details["gated_by"]; present {
			t.Errorf("gated_by = %v, want absent", v)
		}
		deps, _ := details["dependencies"].([]interface{})
		if len(deps) != 1 {
			t.Fatalf("dependencies = %v, want only the local epic", details["dependencies"])
		}
		if dep, _ := deps[0].(map[string]interface{}); dep["id"] != epic.ID {
			t.Errorf("dependencies[0].id = %v, want %s", dep["id"], epic.ID)
		}
		edges, _ := details["unresolvable_dependency_edges"].([]interface{})
		var got []string
		for _, e := range edges {
			edge, _ := e.(map[string]interface{})
			got = append(got, fmt.Sprintf("%v/%v", edge["depends_on_id"], edge["type"]))
		}
		if want := []string{"liveop-epic/parent-child", "liveop-gate/blocks"}; !slices.Equal(got, want) {
			t.Errorf("unresolvable_dependency_edges = %v, want %v", got, want)
		}
	})

	// NEGATIVE CONTROL, and it is the load-bearing half: a fully local edge
	// must leave the field unset and stderr silent. Without it every
	// assertion above is satisfied by a build that reports every issue as
	// having unresolvable edges.
	t.Run("fully_local_control", func(t *testing.T) {
		bdDep(t, bd, dir, "add", anchor.ID, local.ID, "--type", "blocks")

		details := bdShowDetails(t, bd, dir, anchor.ID)
		if v, present := details["unresolvable_dependencies"]; present {
			t.Errorf("unresolvable_dependencies = %v on a fully local edge, want absent", v)
		}
		if v, present := details["unresolvable_dependency_edges"]; present {
			t.Errorf("unresolvable_dependency_edges = %v on a fully local edge, want absent", v)
		}
		// The blocker has no outbound edge at all.
		if v, present := bdShowDetails(t, bd, dir, local.ID)["unresolvable_dependency_edges"]; present {
			t.Errorf("unresolvable_dependency_edges = %v on an issue with no outbound edge, want absent", v)
		}
		deps, _ := details["dependencies"].([]interface{})
		if len(deps) != 1 {
			t.Errorf("dependencies = %v, want the one local edge", details["dependencies"])
		}

		_, stderr := runShowSplit(t, bd, dir, anchor.ID)
		if strings.Contains(stderr, "no row in this database") {
			t.Errorf("bd show warned about a fully local edge:\nstderr:\n%s", stderr)
		}
	})
}

// runShowSplit runs `bd show <id>` keeping stdout and stderr apart, which the
// shared helpers deliberately merge. The split is the whole point here: the
// notice's contract is that it appears on stderr and nowhere else.
func runShowSplit(t *testing.T, bd, dir, id string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(bd, "show", id)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	outBuf, errBuf, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}
