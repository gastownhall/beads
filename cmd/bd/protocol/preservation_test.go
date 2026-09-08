package protocol

import "testing"

// TestProtocol_LabelsPreservedAcrossUpdate asserts that labels added to an
// issue are not lost when the issue is updated.
func TestProtocol_LabelsPreservedAcrossUpdate(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Labeled issue", "--type", "task")
	w.run("label", "add", id, "frontend")
	w.run("label", "add", id, "urgent")

	// Update an unrelated field
	w.run("update", id, "--title", "Labeled issue (renamed)")

	issue := w.showJSON(id)

	requireStringSetEqual(t, getStringSlice(issue, "labels"),
		[]string{"frontend", "urgent"}, "labels after title update")
}

// TestProtocol_DepsPreservedAcrossUpdate asserts that dependencies are not
// lost when an issue is updated.
func TestProtocol_DepsPreservedAcrossUpdate(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	idA := w.create("--title", "Blocker", "--type", "task")
	idB := w.create("--title", "Blocked", "--type", "task")
	w.run("dep", "add", idB, idA)

	// Update an unrelated field
	w.run("update", idB, "--title", "Blocked (renamed)")

	issue := w.showJSON(idB)

	requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
		[]depEdge{{issueID: idB, dependsOnID: idA}}, "deps after title update")
}

// TestProtocol_CommentsPreservedAcrossUpdate asserts that comments are not
// lost when an issue is updated.
func TestProtocol_CommentsPreservedAcrossUpdate(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Commented issue", "--type", "task")
	w.run("comments", "add", id, "Important design note")
	w.run("comments", "add", id, "Follow-up from review")

	// Update an unrelated field
	w.run("update", id, "--title", "Commented issue (renamed)")

	issue := w.showJSON(id)

	requireCommentTextsEqual(t, getObjectSlice(issue, "comments"),
		[]string{"Important design note", "Follow-up from review"},
		"comments after title update")
}

// TestProtocol_ScalarUpdatePreservesRelationalData asserts that updating
// scalar fields (title, priority, description, assignee, notes) does NOT
// silently drop labels, dependencies, or comments from an issue.
//
// Invariant: for any issue with labels L, deps D, and comments C,
// running bd update <id> --title "..." must leave L, D, and C unchanged.
//
// This is the single most important data-integrity invariant. A violation
// means any routine update can cause silent data loss.
func TestProtocol_ScalarUpdatePreservesRelationalData(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id1 := w.create("--title", "Data-rich issue", "--type", "feature", "--priority", "1")
	id2 := w.create("--title", "Dep target", "--type", "task")

	// Set up relational data
	w.run("label", "add", id1, "important")
	w.run("label", "add", id1, "v2")
	w.run("label", "add", id1, "frontend")
	w.run("dep", "add", id1, id2)
	w.run("comments", "add", id1, "Design review notes")
	w.run("comments", "add", id1, "Implementation started")

	// Rapid-fire scalar updates — each must preserve relational data
	w.run("update", id1, "--title", "Data-rich issue v2")
	w.run("update", id1, "--priority", "0")
	w.run("update", id1, "--description", "Updated description")
	w.run("update", id1, "--assignee", "alice")
	w.run("update", id1, "--notes", "Updated notes")

	// Verify via show --json
	issue := w.showJSON(id1)

	t.Run("labels_preserved", func(t *testing.T) {
		requireStringSetEqual(t, getStringSlice(issue, "labels"),
			[]string{"important", "v2", "frontend"},
			"labels after 5 scalar updates")
	})

	t.Run("deps_preserved", func(t *testing.T) {
		requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
			[]depEdge{{issueID: id1, dependsOnID: id2}},
			"deps after 5 scalar updates")
	})

	t.Run("comments_preserved", func(t *testing.T) {
		requireCommentTextsEqual(t, getObjectSlice(issue, "comments"),
			[]string{"Design review notes", "Implementation started"},
			"comments after 5 scalar updates")
	})
}

// TestProtocol_StatusTransitionsPreserveLabels asserts that cycling through
// status transitions does not lose labels.
//
// Invariant: labels, deps, comments survive all status transitions.
func TestProtocol_StatusTransitionsPreserveLabels(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	a := w.create("--title", "Status cycle", "--type", "task", "--priority", "2")
	w.run("label", "add", a, "test-label")
	w.run("comments", "add", a, "Important note")

	// open → in_progress → open → closed → reopen
	w.run("update", a, "--status", "in_progress")
	w.run("update", a, "--status", "open")
	w.run("close", a)
	w.run("reopen", a)

	shown := w.showJSON(a)

	// Labels preserved
	labels := getStringSlice(shown, "labels")
	if len(labels) != 1 || labels[0] != "test-label" {
		t.Errorf("labels after status cycle: got %v, want [test-label]", labels)
	}

	// Comments preserved
	comments := getObjectSlice(shown, "comments")
	if len(comments) != 1 {
		t.Errorf("comments after status cycle: got %d, want 1", len(comments))
	}
}

// TestProtocol_DeleteCleansUpDeps asserts that deleting an issue removes
// all references to it from other issues' dependency lists.
//
// Invariant: after bd delete X, no other issue should have X in its
// dependencies as shown by bd show --json.
func TestProtocol_DeleteCleansUpDeps(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	idA := w.create("--title", "Survivor A", "--type", "task")
	idB := w.create("--title", "Will be deleted", "--type", "task")
	idC := w.create("--title", "Survivor C", "--type", "task")

	w.run("dep", "add", idB, idA) // B depends on A
	w.run("dep", "add", idC, idB) // C depends on B

	w.run("delete", idB, "--force")

	// B should not be queryable after deletion
	_, err := w.tryRun("show", idB, "--json")
	if err == nil {
		t.Errorf("deleted issue %s should not be queryable via show", idB)
	}

	// Surviving issues should not reference B in their dependencies
	for _, survivorID := range []string{idA, idC} {
		issue := w.showJSON(survivorID)
		deps := getObjectSlice(issue, "dependencies")
		for _, dep := range deps {
			depID, _ := dep["depends_on_id"].(string)
			if depID == "" {
				depID, _ = dep["id"].(string)
			}
			if depID == idB {
				t.Errorf("issue %s still has dangling dependency on deleted %s", survivorID, idB)
			}
		}
	}
}
