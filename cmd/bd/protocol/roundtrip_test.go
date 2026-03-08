package protocol

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestProtocol_ImportPreservesRelationalData asserts that relational data
// (labels, dependencies, comments) set via CLI commands survives and is
// queryable via bd show --json.
//
// Invariant: create → add labels/deps/comments → show --json returns all data.
func TestProtocol_ImportPreservesRelationalData(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id1 := w.create("--title", "Feature with data", "--type", "feature", "--priority", "1")
	id2 := w.create("--title", "Dependency target", "--type", "task", "--priority", "2")

	w.run("label", "add", id1, "important")
	w.run("label", "add", id1, "v2")
	w.run("label", "add", id2, "backend")

	w.run("dep", "add", id1, id2) // feature depends on dep-target

	w.run("comments", "add", id1, "Design notes for the feature")
	w.run("comments", "add", id1, "Review feedback from team")

	// Verify via bd show --json
	featShow := w.showJSON(id1)
	depTargetShow := w.showJSON(id2)

	t.Run("labels", func(t *testing.T) {
		requireStringSetEqual(t, getStringSlice(featShow, "labels"),
			[]string{"important", "v2"}, "feature labels via show --json")

		requireStringSetEqual(t, getStringSlice(depTargetShow, "labels"),
			[]string{"backend"}, "dep-target labels via show --json")
	})

	t.Run("dependencies", func(t *testing.T) {
		wantEdges := []depEdge{{issueID: id1, dependsOnID: id2}}
		requireDepEdgesEqual(t, getObjectSlice(featShow, "dependencies"),
			wantEdges, "feature deps via show --json")
	})

	t.Run("comments", func(t *testing.T) {
		wantTexts := []string{
			"Design notes for the feature",
			"Review feedback from team",
		}
		requireCommentTextsEqual(t, getObjectSlice(featShow, "comments"),
			wantTexts, "feature comments via show --json")
	})
}

// TestProtocol_FieldsRoundTrip asserts that every field settable via CLI
// survives create/update → show --json. This is a data integrity invariant:
// if the CLI accepts a value, show must reflect it.
func TestProtocol_FieldsRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Round-trip subject",
		"--type", "feature",
		"--priority", "1",
		"--description", "Detailed description",
		"--design", "Hexagonal architecture",
		"--acceptance", "All tests pass",
		"--notes", "Initial planning notes",
		"--assignee", "alice",
		"--estimate", "180",
	)

	// Update fields that aren't available on create
	w.run("update", id, "--due", "2099-03-15")
	w.run("update", id, "--defer", "2099-01-15")

	issue := w.showJSON(id)

	// Assert each field
	assertField(t, issue, "title", "Round-trip subject")
	assertField(t, issue, "issue_type", "feature")
	assertFieldFloat(t, issue, "priority", 1)
	assertField(t, issue, "description", "Detailed description")
	assertField(t, issue, "design", "Hexagonal architecture")
	assertField(t, issue, "acceptance_criteria", "All tests pass")
	assertField(t, issue, "notes", "Initial planning notes")
	assertField(t, issue, "assignee", "alice")
	assertFieldFloat(t, issue, "estimated_minutes", 180)

	// Date fields: date-only inputs are parsed as local midnight, then stored as UTC.
	// The UTC representation may show the previous day in west-of-UTC timezones.
	dueLocal, _ := time.ParseInLocation("2006-01-02", "2099-03-15", time.Local)
	deferLocal, _ := time.ParseInLocation("2006-01-02", "2099-01-15", time.Local)
	assertFieldPrefix(t, issue, "due_at", dueLocal.UTC().Format("2006-01-02"))
	assertFieldPrefix(t, issue, "defer_until", deferLocal.UTC().Format("2006-01-02"))
}

// TestProtocol_MetadataRoundTrip asserts that JSON metadata set via
// bd update --metadata survives in show --json output.
func TestProtocol_MetadataRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Metadata carrier", "--type", "task")
	w.run("update", id, "--metadata", `{"component":"auth","risk":"high"}`)

	issue := w.showJSON(id)

	md, exists := issue["metadata"]
	if !exists {
		t.Fatal("metadata field missing from show --json")
	}

	// Metadata may be a string or a parsed object depending on JSON serialization
	switch v := md.(type) {
	case map[string]any:
		if v["component"] != "auth" || v["risk"] != "high" {
			t.Errorf("metadata content mismatch: got %v", v)
		}
	case string:
		if !strings.Contains(v, "auth") || !strings.Contains(v, "high") {
			t.Errorf("metadata content mismatch: got %q", v)
		}
	default:
		t.Errorf("unexpected metadata type %T: %v", md, md)
	}
}

// TestProtocol_SpecIDRoundTrip asserts that spec_id set via bd update --spec-id
// survives in show --json output.
func TestProtocol_SpecIDRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Spec carrier", "--type", "task")
	w.run("update", id, "--spec-id", "RFC-007")

	issue := w.showJSON(id)

	specID, ok := issue["spec_id"].(string)
	if !ok || specID == "" {
		t.Fatal("spec_id field missing or empty from show --json")
	}
	if specID != "RFC-007" {
		t.Errorf("spec_id = %q, want %q", specID, "RFC-007")
	}
}

// TestProtocol_CloseReasonRoundTrip asserts that close_reason survives
// close → show --json.
func TestProtocol_CloseReasonRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Closeable", "--type", "bug", "--priority", "2")
	w.run("close", id, "--reason", "Fixed in commit abc123")

	issue := w.showJSON(id)

	reason, ok := issue["close_reason"].(string)
	if !ok || reason == "" {
		t.Fatal("close_reason missing or empty from show --json after bd close --reason")
	}
	if reason != "Fixed in commit abc123" {
		t.Errorf("close_reason = %q, want %q", reason, "Fixed in commit abc123")
	}
}

// TestProtocol_ParentChildDepShowRoundTrip asserts that when a child issue
// is created via --parent, the dependency is visible via bd show --json
// in both directions: the child's dependencies reference the parent,
// and the parent's dependents reference the child.
func TestProtocol_ParentChildDepShowRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	parent := w.create("--title", "Epic parent", "--type", "epic", "--priority", "1")
	child := w.create("--title", "Child task", "--type", "task", "--priority", "2", "--parent", parent)

	childIssue := w.showJSON(child)
	parentIssue := w.showJSON(parent)

	// Child must have a dependency pointing to parent
	t.Run("child_references_parent", func(t *testing.T) {
		childDeps := getObjectSlice(childIssue, "dependencies")
		found := false
		for _, dep := range childDeps {
			// show --json embeds the depended-on issue; "id" is the target
			depID, _ := dep["id"].(string)
			if depID == "" {
				depID, _ = dep["depends_on_id"].(string)
			}
			if depID == parent {
				found = true
				// Verify it's a parent-child type
				depType, _ := dep["dependency_type"].(string)
				if depType == "" {
					depType, _ = dep["type"].(string)
				}
				if depType != "parent-child" {
					t.Errorf("child→parent dep type = %q, want %q", depType, "parent-child")
				}
			}
		}
		if !found {
			t.Errorf("child %s has no dependency referencing parent %s (got %d deps)",
				child, parent, len(childDeps))
		}
	})

	// Parent must show the child in its dependents list
	t.Run("parent_shows_child_as_dependent", func(t *testing.T) {
		parentDependents := getObjectSlice(parentIssue, "dependents")
		found := false
		for _, dep := range parentDependents {
			depID, _ := dep["id"].(string)
			if depID == child {
				found = true
			}
		}
		if !found {
			t.Errorf("parent %s does not list child %s in dependents (got %d dependents)",
				parent, child, len(parentDependents))
		}
	})
}

// TestProtocol_LabelAddRemoveRoundTrip asserts that labels can be added
// and removed correctly.
//
// Invariant: label add + label remove is a no-op on the label set.
func TestProtocol_LabelAddRemoveRoundTrip(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	a := w.create("--title", "Label round-trip", "--type", "task")

	w.run("label", "add", a, "bug-fix")
	w.run("label", "add", a, "urgent")
	w.run("label", "add", a, "frontend")

	// Remove one
	w.run("label", "remove", a, "urgent")

	shown := w.showJSON(a)
	labels := getStringSlice(shown, "labels")
	sort.Strings(labels)

	want := []string{"bug-fix", "frontend"}
	if !slices.Equal(labels, want) {
		t.Errorf("labels after add 3, remove 1: got %v, want %v", labels, want)
	}
}
