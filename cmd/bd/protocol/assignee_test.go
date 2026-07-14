package protocol

import "testing"

// TestProtocol_CreateDoesNotSelfAssign pins protocol clause D1.6: a plain
// `bd create` yields an UNASSIGNED issue, even though an actor identity is
// resolvable (D2.2) and is recorded in created_by. assignee is the work-queue
// field the unassigned-only claim rule (L3.1) reads, so a self-assigning create
// would make every freshly created issue unclaimable by a worker fleet — the
// folk belief this clause corrects.
func TestProtocol_CreateDoesNotSelfAssign(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// The workspace git config sets user.name=protocol-test, so an actor
	// identity IS resolvable; the point is that it lands in created_by only.
	id := w.create("--title", "Fresh work", "--type", "task")

	issue := w.showJSON(id)
	if assignee, ok := issue["assignee"].(string); ok && assignee != "" {
		t.Errorf("D1.6: bd create self-assigned (assignee = %q); a fresh issue MUST be unassigned", assignee)
	}
	assertField(t, issue, "created_by", "protocol-test")

	// The observable consequence: the issue is immediately claimable, i.e. it
	// shows up in the unassigned ready front a worker claims from.
	out := w.run("ready", "--unassigned", "--json")
	if findByID(parseJSONOutput(t, out), id) == nil {
		t.Errorf("D1.6: freshly created %s is missing from `bd ready --unassigned`; it must be immediately claimable", id)
	}

	// An explicit --actor does not change this: it selects the actor identity,
	// not the assignee.
	id2 := w.create("--title", "Fresh work, explicit actor", "--type", "task", "--actor", "alice")
	issue2 := w.showJSON(id2)
	if assignee, ok := issue2["assignee"].(string); ok && assignee != "" {
		t.Errorf("D1.6: bd create --actor alice set assignee = %q; --actor MUST NOT self-assign", assignee)
	}
	assertField(t, issue2, "created_by", "alice")
}

// TestProtocol_StatusUpdatePreservesAssignee pins protocol clause D2.4:
// `bd update` mutates only the fields the caller named. Specifically, a status
// change MUST NOT touch assignee — the Gas Station fleet relies on this when it
// parks an in_progress unit back to open without releasing its owner, and the
// inverse (status update silently clearing the assignee) would hand a live
// worker's unit to the next claimer.
func TestProtocol_StatusUpdatePreservesAssignee(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Assigned work", "--type", "task")

	w.run("update", id, "-a", "worker-7", "--status", "in_progress")
	issue := w.showJSON(id)
	assertField(t, issue, "assignee", "worker-7")
	assertField(t, issue, "status", "in_progress")

	// in_progress -> open: status changes, assignee survives.
	w.run("update", id, "--status", "open")
	issue = w.showJSON(id)
	assertField(t, issue, "status", "open")
	assertField(t, issue, "assignee", "worker-7")

	// The same holds for a priority-only update (no status in the command).
	w.run("update", id, "-p", "0")
	issue = w.showJSON(id)
	assertField(t, issue, "assignee", "worker-7")
	assertFieldFloat(t, issue, "priority", 0)

	// Only an explicit `-a ""` clears the assignee (D1.6).
	w.run("update", id, "-a", "")
	issue = w.showJSON(id)
	if assignee, ok := issue["assignee"].(string); ok && assignee != "" {
		t.Errorf(`D1.6: bd update -a "" left assignee = %q, want cleared`, assignee)
	}
}
