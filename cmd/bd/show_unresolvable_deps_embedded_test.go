//go:build cgo

package main

import (
	"os"
	"os/exec"
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
		name      string
		target    string
		depTyp    string
		wantLabel string
	}{
		{"external_reference", "external:liveop:liveop-kmf", "tracks", "(external)"},
		{"cross_repo_prefix", "liveop-kmf", "blocks", "(cross-repo)"},
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

			// The member that makes the count navigable (bd-hkpas7, #5272):
			// the raw stored edge itself, sorted and typed, beside the count
			// that until now was a number with no referent. Generic decode —
			// pinning the wire member, not a struct the compiler could make
			// this pass against.
			edges, present := details["unresolvable_dependency_edges"].([]interface{})
			if !present || len(edges) != 1 {
				t.Fatalf("unresolvable_dependency_edges = %v, want the one raw edge the count names",
					details["unresolvable_dependency_edges"])
			}
			edge, isMap := edges[0].(map[string]interface{})
			if !isMap || edge["depends_on_id"] != tc.target {
				t.Errorf("unresolvable_dependency_edges[0] = %v, want depends_on_id %s",
					edges[0], tc.target)
			}
			if edge["type"] != tc.depTyp {
				t.Errorf("unresolvable_dependency_edges[0].type = %v, want %s", edge["type"], tc.depTyp)
			}
			if edge["issue_id"] != subject.ID {
				t.Errorf("unresolvable_dependency_edges[0].issue_id = %v, want the subject %s", edge["issue_id"], subject.ID)
			}

			// Text mode (bd-hkpas7, #5272): the raw edge now RENDERS —
			// the target id appears in its section on stdout with its
			// classification label, and the outbound warning is gone,
			// because the warning counts only the gaps the screen did not
			// cover (AC-2). Before this the id appeared nowhere on stdout
			// and the only disclosure was the stderr count.
			stdout, stderr := runShowSplit(t, bd, dir, subject.ID)
			if !strings.Contains(stdout, tc.target) {
				t.Errorf("bd show stdout did not name the unresolvable edge target %s:\n%s", tc.target, stdout)
			}
			if !strings.Contains(stdout, tc.wantLabel) {
				t.Errorf("bd show stdout did not classify the target as %s:\n%s", tc.wantLabel, stdout)
			}
			if strings.Contains(stderr, "no row in this database") {
				t.Errorf("bd show still warns about an edge it just printed:\nstderr:\n%s", stderr)
			}
			if strings.Contains(stderr, "For raw edge records") {
				t.Errorf("bd show still offers the raw-record recovery command for an edge it just printed:\nstderr:\n%s", stderr)
			}
		})
	}

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
