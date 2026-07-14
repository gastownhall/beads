package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_DepReAddSameTypeIsIdempotent pins the first half of protocol
// clause G1.3: an edge is identified by (issue_id, depends_on_id), so re-adding
// the same pair with the same type is idempotent — it succeeds and leaves
// exactly ONE edge. Agents (and JSONL import) re-assert edges routinely; a
// duplicate row would double-count blockers and corrupt the ready front.
func TestProtocol_DepReAddSameTypeIsIdempotent(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	blocked := w.create("--title", "Blocked", "--type", "task")
	blocker := w.create("--title", "Blocker", "--type", "task")

	w.run("dep", "add", blocked, blocker)
	// Same pair, same type (blocks is the default) — must succeed, not error.
	w.run("dep", "add", blocked, blocker)
	// Explicitly naming the same type must also succeed.
	w.run("dep", "add", blocked, blocker, "--type", "blocks")

	issue := w.showJSON(blocked)
	requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
		[]depEdge{{issueID: blocked, dependsOnID: blocker}},
		"G1.3: dependencies after three identical `dep add` calls")
}

// TestProtocol_DepReAddDifferentTypeIsAnError pins the second half of G1.3: the
// same (issue_id, depends_on_id) pair with a DIFFERENT type is an error, not a
// silent retype — the caller must remove the edge first. A silent retype would
// let `dep add --type discovered-from` quietly demote a blocking edge and make
// a blocked issue ready.
func TestProtocol_DepReAddDifferentTypeIsAnError(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	from := w.create("--title", "Discovered work", "--type", "task")
	to := w.create("--title", "Original work", "--type", "task")

	w.run("dep", "add", from, to, "--type", "blocks")

	out, code := w.runExpectError("dep", "add", from, to, "--type", "discovered-from")
	if code == 0 {
		t.Fatalf("G1.3: conflicting dep re-add exited 0, want non-zero\n%s", out)
	}
	if !strings.Contains(out, "already exists with type") {
		t.Errorf("G1.3: conflict error should name the existing type; got:\n%s", out)
	}

	// The original edge is untouched by the rejected call.
	issue := w.showJSON(from)
	requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
		[]depEdge{{issueID: from, dependsOnID: to}},
		"G1.3: dependencies after a rejected retype")
	if !depHasType(t, w, from, to, "blocks") {
		t.Errorf("G1.3: rejected retype changed the edge type of %s -> %s; it must stay \"blocks\"", from, to)
	}

	// Remove-then-re-add is the sanctioned retype path.
	w.run("dep", "remove", from, to)
	w.run("dep", "add", from, to, "--type", "discovered-from")
	if !depHasType(t, w, from, to, "discovered-from") {
		t.Errorf("G1.3: after remove + re-add, %s -> %s should have type \"discovered-from\"", from, to)
	}
}

// depHasType reports whether `bd dep list <id> --json` carries an edge
// id -> dependsOn with the given type. A single-id `dep list` emits the
// depended-on ISSUES, each carrying the edge type as "dependency_type".
func depHasType(t *testing.T, w *workspace, id, dependsOn, want string) bool {
	t.Helper()
	out := w.run("dep", "list", id, "--json")
	for _, obj := range parseJSONOutput(t, out) {
		if obj["id"] != dependsOn {
			continue
		}
		if typ, ok := obj["dependency_type"].(string); ok && typ == want {
			return true
		}
	}
	return false
}
