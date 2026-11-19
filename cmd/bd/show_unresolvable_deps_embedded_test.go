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
// rendered no dependency section at all. Since GH#5850 the text render shows
// the far end in its relation section with "(not in this database)" — the
// exact edge, visible where the reader is looking, instead of a stderr count
// of edges they cannot see.
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
		name   string
		target string
		depTyp string
	}{
		{"external_reference", "external:liveop:liveop-kmf", "tracks"},
		{"cross_repo_prefix", "liveop-kmf", "blocks"},
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

			// GH#5850: the exact stored record travels beside the
			// count — one record, the target id the caller typed,
			// the type the edge carries, and the anchor it belongs
			// to. No stub issue, no invented status.
			edges, isList := details["unresolvable_dependency_edges"].([]interface{})
			if !isList || len(edges) != 1 {
				t.Fatalf("unresolvable_dependency_edges = %v, want the one stored record",
					details["unresolvable_dependency_edges"])
			}
			rec, isMap := edges[0].(map[string]interface{})
			if !isMap || rec["depends_on_id"] != tc.target || rec["issue_id"] != subject.ID || rec["type"] != tc.depTyp {
				t.Fatalf("unresolvable_dependency_edges[0] = %v, want the exact stored record for %q",
					edges[0], tc.target)
			}

			// Text mode: the far end renders in its relation section on
			// stdout (GH#5850 AC-3), so the reader sees the edge where
			// the local ones print. The old stderr count-only warning is
			// GONE for the outbound direction — asserting that too, or a
			// regression that restores the warning alongside the render
			// passes every check above.
			stdout, stderr := runShowSplit(t, bd, dir, subject.ID)
			if !strings.Contains(stdout, tc.target+" (not in this database)") {
				t.Errorf("bd show stdout did not render the far end in its relation section:\nstdout:\n%s", stdout)
			}
			// The section for the edge's own type printed — this is the
			// lone-section-entry case: without the fix there was no
			// dependency section at all.
			heading := map[string]string{"tracks": "TRACKS", "blocks": "DEPENDS ON"}[tc.depTyp]
			if !strings.Contains(stdout, heading) {
				t.Errorf("bd show stdout did not print the %s section:\nstdout:\n%s", heading, stdout)
			}
			if strings.Contains(stderr, "edge(s) whose far end") {
				t.Errorf("the redundant outbound stderr warning survived the render:\nstderr:\n%s", stderr)
			}
			if strings.Contains(stdout, "no row in this database") {
				t.Errorf("the legacy warning text reached stdout:\n%s", stdout)
			}
		})
	}

	// The watch route is the second of the three text renders
	// (displayShowIssueReturn in show_display.go). It reads the same combined
	// store query as the direct route, so the far end must appear there too
	// — or --watch silently regressed to dropping the edge while plain
	// `bd show` kept the fix.
	t.Run("watch_route_renders_far_end", func(t *testing.T) {
		subject := bdCreate(t, bd, dir, "Watched subject", "--type", "task")
		bdDep(t, bd, dir, "add", subject.ID, "liveop-kmf", "--type", "blocks")

		out := runShowWatchOnce(t, bd, dir, bdEnv(dir), subject.ID, "--watch")
		if !strings.Contains(out, "liveop-kmf (not in this database)") {
			t.Errorf("bd show --watch did not render the far end in its relation section:\n%s", out)
		}
		if !strings.Contains(out, "DEPENDS ON") {
			t.Errorf("bd show --watch did not print the relation section:\n%s", out)
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
		deps, _ := details["dependencies"].([]interface{})
		if len(deps) != 1 {
			t.Errorf("dependencies = %v, want the one local edge", details["dependencies"])
		}

		stdout, stderr := runShowSplit(t, bd, dir, anchor.ID)
		if strings.Contains(stderr, "no row in this database") {
			t.Errorf("bd show warned about a fully local edge:\nstderr:\n%s", stderr)
		}
		if strings.Contains(stdout, "(not in this database)") {
			t.Errorf("bd show marked a fully local edge as missing:\nstdout:\n%s", stdout)
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
