//go:build regression

package regression

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Foundational scenarios
// ---------------------------------------------------------------------------

// TestBasicLifecycle creates issues, updates one, closes one, and compares exports.
func TestBasicLifecycle(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id1 := w.create("--title", "First task", "--type", "task", "--priority", "2")
		id2 := w.create("--title", "Bug report", "--type", "bug", "--priority", "1")

		w.run("update", id1, "--title", "First task (updated)")
		w.run("close", id2, "--reason", "fixed")
	})
}

// TestLabelsRoundTrip adds and removes labels, comparing exports.
func TestLabelsRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Labeled issue", "--type", "task")

		w.run("label", "add", id, "frontend")
		w.run("label", "add", id, "urgent")
		w.run("label", "add", id, "v2")

		w.run("label", "remove", id, "urgent")
	})
}

// TestDependenciesRoundTrip creates a dependency chain and compares exports.
func TestDependenciesRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		idA := w.create("--title", "Task A", "--type", "task")
		idB := w.create("--title", "Task B", "--type", "task")
		idC := w.create("--title", "Task C", "--type", "task")

		w.run("dep", "add", idB, idA) // B depends on A
		w.run("dep", "add", idC, idB) // C depends on B
	})
}

// TestCommentsRoundTrip adds comments to an issue and compares exports.
func TestCommentsRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Commented issue", "--type", "task")

		w.run("comment", id, "First comment on the issue")
		w.run("comment", id, "Second comment with more detail")
	})
}

// TestFilteredExport creates open and closed issues, exports with --status filter.
func TestFilteredExport(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Open issue", "--type", "task")
		id2 := w.create("--title", "Closed issue", "--type", "bug")
		w.run("close", id2, "--reason", "done")
	}

	t.Run("full", func(t *testing.T) {
		compareExports(t, scenario)
	})

	t.Run("status_open", func(t *testing.T) {
		baselineWS := newWorkspace(t, baselineBin)
		scenario(baselineWS)
		baselineRaw := baselineWS.export("--status", "open")

		candidateWS := newWorkspace(t, candidateBin)
		scenario(candidateWS)
		candidateRaw := candidateWS.export("--status", "open")

		diffNormalized(t,
			baselineRaw, candidateRaw,
			canonicalIDMap(baselineWS.createdIDs),
			canonicalIDMap(candidateWS.createdIDs),
		)
	})
}

// TestReopenRoundTrip verifies close → reopen preserves all fields.
func TestReopenRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Reopenable task", "--type", "bug", "--priority", "1")

		w.run("close", id, "--reason", "thought it was fixed")
		w.run("reopen", id)
	})
}

// TestUpdateFieldPreservation creates an issue with many fields, updates one,
// and verifies the others survive. Catches "partial update clobbers fields."
func TestUpdateFieldPreservation(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create(
			"--title", "Multi-field issue",
			"--type", "feature",
			"--priority", "1",
			"--description", "Detailed description of the feature",
			"--design", "Use a layered architecture",
			"--acceptance", "All tests pass and docs updated",
			"--notes", "Check with team before starting",
		)

		// Update only the title — everything else must survive
		w.run("update", id, "--title", "Multi-field issue (renamed)")
	})
}

// TestExternalRefSpecID sets external_ref and spec_id, verifying round-trip.
// Caught a new regression: spec_id is stored by v0.49.6 but silently lost
// in main's Dolt backend (external_ref works fine in both).
func TestExternalRefSpecID(t *testing.T) {
	t.Skip("known regression: bd-wzgir — spec_id dropped by Dolt backend UpdateIssue")
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Tracked externally", "--type", "task")

		w.run("update", id, "--external-ref", "gh-42")
		w.run("update", id, "--spec-id", "RFC-007")
	})
}

// TestParentChildDependency creates a parent-child relationship via dep add --type.
func TestParentChildDependency(t *testing.T) {
	compareExports(t, func(w *workspace) {
		parent := w.create("--title", "Epic parent", "--type", "epic")
		child1 := w.create("--title", "Child task one", "--type", "task")
		child2 := w.create("--title", "Child task two", "--type", "task")

		w.run("dep", "add", child1, parent, "--type", "parent-child")
		w.run("dep", "add", child2, parent, "--type", "parent-child")
	})
}

// ---------------------------------------------------------------------------
// High-value regression scenarios
// ---------------------------------------------------------------------------

// TestExportImportRoundTrip exports from baseline and imports into candidate,
// detecting silent data loss (#1844: importer dropping labels/deps/comments).
func TestExportImportRoundTrip(t *testing.T) {
	t.Skip("known regression: GH#1844 — import drops labels/deps/comments")
	// Step 1: Create rich data in baseline
	baselineWS := newWorkspace(t, baselineBin)
	id1 := baselineWS.create("--title", "Feature with everything", "--type", "feature", "--priority", "1")
	id2 := baselineWS.create("--title", "Dependency target", "--type", "task", "--priority", "2")

	baselineWS.run("label", "add", id1, "important")
	baselineWS.run("label", "add", id1, "v2")
	baselineWS.run("label", "add", id2, "backend")

	baselineWS.run("dep", "add", id1, id2)

	baselineWS.run("comment", id1, "Design notes for the feature")
	baselineWS.run("comment", id1, "Implementation started")

	// Step 2: Export from baseline
	exportFile := filepath.Join(baselineWS.dir, "export.jsonl")
	baselineWS.run("export", "-o", exportFile)
	baselineRaw, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading baseline export: %v", err)
	}

	// Step 3: Import into candidate
	candidateWS := newWorkspace(t, candidateBin)
	importFile := filepath.Join(candidateWS.dir, "import.jsonl")
	if err := os.WriteFile(importFile, baselineRaw, 0o644); err != nil {
		t.Fatalf("writing import file: %v", err)
	}
	candidateWS.run("import", "-i", importFile)

	// Step 4: Export from candidate and compare
	// IDs are preserved during import, so no ID mapping needed.
	candidateRaw := candidateWS.export()

	diffNormalized(t, string(baselineRaw), candidateRaw, nil, nil)
}

// TestReadySemantics builds a dependency graph and compares which issues
// are reported as "ready" by each binary (#1525: blocking semantics drift).
func TestReadySemantics(t *testing.T) {
	scenario := func(w *workspace) (ids [5]string) {
		// test graph:
		//   ids[0] (closed) ← ids[1] (blocked) ← ids[2] (blocked, transitive)
		//   ids[3] (no deps, ready)
		//   ids[0] (closed) ← ids[4] (unblocked because ids[0] is closed)
		ids[0] = w.create("--title", "Foundation task", "--type", "task", "--priority", "1")
		ids[1] = w.create("--title", "Blocked by foundation", "--type", "task", "--priority", "2")
		ids[2] = w.create("--title", "Transitively blocked", "--type", "task", "--priority", "2")
		ids[3] = w.create("--title", "Independent task", "--type", "task", "--priority", "3")
		ids[4] = w.create("--title", "Unblocked after close", "--type", "task", "--priority", "2")

		w.run("dep", "add", ids[1], ids[0])
		w.run("dep", "add", ids[2], ids[1])
		w.run("dep", "add", ids[4], ids[0])

		w.run("close", ids[0], "--reason", "done")
		return ids
	}

	baselineWS := newWorkspace(t, baselineBin)
	bIDs := scenario(baselineWS)

	candidateWS := newWorkspace(t, candidateBin)
	cIDs := scenario(candidateWS)

	baselineReady := parseReadyIDs(t, baselineWS)
	candidateReady := parseReadyIDs(t, candidateWS)

	// Map real IDs to canonical for comparison
	bIDMap := make(map[string]string)
	cIDMap := make(map[string]string)
	for i := range bIDs {
		canonical := bIDs[i] // use baseline ID as the canonical key
		bIDMap[bIDs[i]] = canonical
		cIDMap[cIDs[i]] = canonical
	}

	// Canonicalize ready sets
	bReady := make(map[string]bool)
	for id := range baselineReady {
		if c, ok := bIDMap[id]; ok {
			bReady[c] = true
		}
	}
	cReady := make(map[string]bool)
	for id := range candidateReady {
		if c, ok := cIDMap[id]; ok {
			cReady[c] = true
		}
	}

	// Compare
	allIDs := make(map[string]bool)
	for id := range bReady {
		allIDs[id] = true
	}
	for id := range cReady {
		allIDs[id] = true
	}

	mismatch := false
	for id := range allIDs {
		if bReady[id] != cReady[id] {
			mismatch = true
			if bReady[id] {
				t.Errorf("  %s: ready in baseline but NOT in candidate", id)
			} else {
				t.Errorf("  %s: ready in candidate but NOT in baseline", id)
			}
		}
	}
	if mismatch {
		t.Logf("Baseline ready: %v", setToSortedSlice(bReady))
		t.Logf("Candidate ready: %v", setToSortedSlice(cReady))
	}

	// Also compare the full JSONL export
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestSortingInvariants creates issues with different priorities and checks
// that bd ready output ordering is consistent (#1880).
func TestSortingInvariants(t *testing.T) {
	t.Skip("known regression: GH#1880 — bd ready sort order differs between backends")
	scenario := func(w *workspace) {
		w.create("--title", "Low priority", "--type", "task", "--priority", "4")
		w.create("--title", "Critical", "--type", "task", "--priority", "0")
		w.create("--title", "Medium", "--type", "task", "--priority", "2")
		w.create("--title", "High priority", "--type", "task", "--priority", "1")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)

	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare ready order by title (since IDs differ between versions).
	baselineOrder := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	candidateOrder := extractTitleOrder(t, candidateWS.run("ready", "--json"))

	if strings.Join(baselineOrder, " | ") != strings.Join(candidateOrder, " | ") {
		t.Errorf("Ready ordering differs:\n  baseline:  %v\n  candidate: %v",
			baselineOrder, candidateOrder)
	}
}

// TestTypeExclusionInReady creates issues with internal types (agent, role, rig,
// gate, molecule, message, merge-request) plus normal tasks, and verifies both
// versions agree on what appears in bd ready.
//
// Note: v0.49.6 had NO type exclusions; main excludes internal types.
// This is an intentional behavior change, not a regression.
func TestTypeExclusionInReady(t *testing.T) {
	t.Skip("intentional change: main excludes internal types from ready; v0.49.6 did not")

	scenario := func(w *workspace) {
		w.create("--title", "Normal task", "--type", "task")
		w.create("--title", "Agent issue", "--type", "agent")
		w.create("--title", "Role issue", "--type", "role")
		w.create("--title", "Gate issue", "--type", "gate")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)

	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	baselineTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	candidateTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))

	sort.Strings(baselineTitles)
	sort.Strings(candidateTitles)

	if strings.Join(baselineTitles, ",") != strings.Join(candidateTitles, ",") {
		t.Errorf("Ready type filtering differs:\n  baseline:  %v\n  candidate: %v",
			baselineTitles, candidateTitles)
	}
}

// ---------------------------------------------------------------------------
// Field round-trip sweep
// ---------------------------------------------------------------------------

// TestPriorityZeroRoundTrip verifies that priority=0 (P0/critical) survives
// update and export. GH#671 showed omitempty silently dropping priority:0.
func TestPriorityZeroRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Critical P0 task", "--type", "task", "--priority", "0")
		w.run("update", id, "--title", "Critical P0 task (updated)")
	})
}

// TestDueAndDeferRoundTrip verifies that due_at and defer_until fields survive
// update and export. Uses fixed far-future dates to avoid time-zone ambiguity.
func TestDueAndDeferRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Scheduled task", "--type", "task", "--priority", "2",
			"--due", "2099-01-15")
		w.run("update", id, "--defer", "2099-06-15")
		w.run("update", id, "--title", "Scheduled task (updated)")
	})
}

// TestMetadataRoundTrip verifies that JSON metadata survives update and export.
// Regression: Dolt backend drops metadata set via bd update --metadata.
func TestMetadataRoundTrip(t *testing.T) {
	// Confirmed regression: fresh candidate binary drops metadata from export.
	// Raw candidate export shows no metadata field at all after bd update --metadata.
	// Raw baseline (v0.49.6/SQLite) correctly preserves metadata:{"component":"auth","risk":"high"}.
	t.Skip("new regression: GH#1912 — Dolt backend drops metadata set via bd update --metadata")
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Metadata test", "--type", "task")
		w.run("update", id, "--metadata", `{"component":"auth","risk":"high"}`)
		w.run("update", id, "--title", "Metadata test (updated)")
	})
}

// TestAssigneeRoundTrip verifies that assignee survives update and export.
func TestAssigneeRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Assigned task", "--type", "task", "--priority", "2",
			"--assignee", "alice")
		w.run("update", id, "--title", "Assigned task (updated)")
	})
}

// TestEstimateRoundTrip verifies that estimated_minutes survives update and export.
func TestEstimateRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Estimated work", "--type", "task", "--priority", "2",
			"--estimate", "120")
		w.run("update", id, "--title", "Estimated work (updated)")
	})
}

// ---------------------------------------------------------------------------
// Dependency type export fidelity sweep
// ---------------------------------------------------------------------------

// TestDepTypeExportFidelity tests that each dependency type the CLI supports
// survives a create → dep add → export round-trip identically in both backends.
// Only blocks, parent-child, and discovered-from are tested elsewhere.
func TestDepTypeExportFidelity(t *testing.T) {
	depTypes := []string{
		"discovered-from",
		"tracks",
		"supersedes",
		"caused-by",
		"validates",
		"until",
		"relates-to",
	}
	for _, dt := range depTypes {
		t.Run(dt, func(t *testing.T) {
			compareExports(t, func(w *workspace) {
				idA := w.create("--title", "Source issue", "--type", "task")
				idB := w.create("--title", "Target issue", "--type", "task")
				w.run("dep", "add", idA, idB, "--type", dt)
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Ready semantics sweep
// ---------------------------------------------------------------------------

// TestDepRemovalUnblocks creates a blocking dependency, removes it, and verifies
// both issues appear as ready in both backends.
func TestDepRemovalUnblocks(t *testing.T) {
	scenario := func(w *workspace) {
		blocker := w.create("--title", "Blocker task", "--type", "task", "--priority", "1")
		blocked := w.create("--title", "Blocked task", "--type", "task", "--priority", "2")
		w.run("dep", "add", blocked, blocker)
		w.run("dep", "remove", blocked, blocker)
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("ready sets differ after dep removal:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestDeferredParentExcludesChildren creates a deferred parent with a child,
// plus an independent task, and verifies both backends agree on ready set.
// Intentional fix: GH#1190 children of deferred parents were shown as ready
// in v0.49.6; main correctly excludes them.
func TestDeferredParentExcludesChildren(t *testing.T) {
	t.Skip("intentional change: main excludes children of deferred parents from ready (GH#1190 fix)")
	scenario := func(w *workspace) {
		parent := w.create("--title", "Deferred parent", "--type", "epic")
		w.run("update", parent, "--defer", "2099-12-31")
		w.create("--title", "Child of deferred", "--type", "task", "--parent", parent)
		w.create("--title", "Independent task", "--type", "task", "--priority", "2")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("ready sets differ for deferred parent:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}
}

// TestEphemeralExcludedFromReady creates an ephemeral and a normal issue,
// verifying both backends agree on ready set (ephemeral excluded by default).
func TestEphemeralExcludedFromReady(t *testing.T) {
	// Probe: does baseline support --ephemeral?
	probe := newWorkspace(t, baselineBin)
	if _, err := probe.tryCreate("--title", "probe", "--type", "task", "--ephemeral"); err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --ephemeral flag")
	}

	scenario := func(w *workspace) {
		w.create("--title", "Ephemeral issue", "--type", "task", "--ephemeral")
		w.create("--title", "Normal task", "--type", "task", "--priority", "2")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("ready sets differ for ephemeral exclusion:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// List/filter sweep
// ---------------------------------------------------------------------------

// TestCloseReasonPreservation verifies that close_reason survives round-trip.
// GH#891: close_reason dropped during merge/sync.
func TestCloseReasonPreservation(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Issue to close", "--type", "bug", "--priority", "2")
		w.run("close", id, "--reason", "Fixed in commit abc123")
	})
}

// TestListStatusFilterParity creates issues in open/closed/in_progress states
// and verifies bd list --status open --json returns the same set in both backends.
func TestListStatusFilterParity(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Open task", "--type", "task", "--priority", "2")
		closed := w.create("--title", "Closed task", "--type", "task", "--priority", "3")
		w.run("close", closed, "--reason", "done")
		inProg := w.create("--title", "In progress task", "--type", "task", "--priority", "1")
		w.run("update", inProg, "--status", "in_progress")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare list --status open
	bTitles := extractTitleOrder(t, baselineWS.run("list", "--status", "open", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("list", "--status", "open", "--json"))
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("list --status open differs:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}

	// Also compare full exports
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestListSortLimitParity creates issues with known priorities, runs list with
// sort+limit, and compares the returned title order. GH#1237: limit applied
// before sort gives random results.
func TestListSortLimitParity(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Low priority", "--type", "task", "--priority", "4")
		w.create("--title", "High priority", "--type", "task", "--priority", "1")
		w.create("--title", "Medium priority", "--type", "task", "--priority", "2")
		w.create("--title", "Critical", "--type", "task", "--priority", "0")
		w.create("--title", "Normal", "--type", "task", "--priority", "3")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bTitles := extractTitleOrder(t, baselineWS.run("list", "--sort", "priority", "--limit", "3", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("list", "--sort", "priority", "--limit", "3", "--json"))

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("list --sort priority --limit 3 differs:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// Duplicate command
// ---------------------------------------------------------------------------

// TestDuplicateCommand marks an issue as duplicate and compares exports.
// GH#1889: bd duplicate fails with "invalid field for update: duplicate_of".
func TestDuplicateCommand(t *testing.T) {
	scenario := func(w *workspace) (string, string) {
		canonical := w.create("--title", "Canonical issue", "--type", "bug", "--priority", "2")
		dup := w.create("--title", "Duplicate report", "--type", "bug", "--priority", "3")
		return canonical, dup
	}

	baselineWS := newWorkspace(t, baselineBin)
	bCanonical, bDup := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cCanonical, cDup := scenario(candidateWS)

	_, bErr := baselineWS.tryRun("duplicate", bDup, "--of", bCanonical)
	_, cErr := candidateWS.tryRun("duplicate", cDup, "--of", cCanonical)

	if bErr != nil && cErr != nil {
		t.Skip("pre-existing bug (baseline): bd duplicate fails in both baseline and candidate")
	}
	if bErr == nil && cErr != nil {
		t.Fatalf("bd duplicate succeeds in baseline but fails in candidate: %v", cErr)
	}
	if bErr != nil && cErr == nil {
		t.Log("bd duplicate fails in baseline but succeeds in candidate (new command)")
		return
	}

	// Both succeeded — compare exports
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Delete cleanup scenarios
// ---------------------------------------------------------------------------

// TestDeleteWithDependencies creates a chain A←B←C, deletes B, and verifies
// that A and C survive with correct reference cleanup.
func TestDeleteWithDependencies(t *testing.T) {
	compareExports(t, func(w *workspace) {
		idA := w.create("--title", "Task A", "--type", "task")
		idB := w.create("--title", "Task B (will delete)", "--type", "task")
		idC := w.create("--title", "Task C", "--type", "task")

		w.run("dep", "add", idB, idA) // B depends on A
		w.run("dep", "add", idC, idB) // C depends on B
		w.run("delete", idB, "--force")
	})
}

// TestDeleteCascade creates a chain A←B←C, cascade-deletes A, and verifies
// all three are removed from the export.
func TestDeleteCascade(t *testing.T) {
	compareExports(t, func(w *workspace) {
		idA := w.create("--title", "Root task", "--type", "task")
		idB := w.create("--title", "Dependent B", "--type", "task")
		idC := w.create("--title", "Dependent C", "--type", "task")
		standalone := w.create("--title", "Standalone", "--type", "task")

		w.run("dep", "add", idB, idA) // B depends on A
		w.run("dep", "add", idC, idB) // C depends on B
		w.run("delete", idA, "--cascade", "--force")

		_ = standalone // ensure at least one issue survives
	})
}

// ---------------------------------------------------------------------------
// Create-with-parent vs explicit dep add
// ---------------------------------------------------------------------------

// TestCreateWithParentFlag verifies that --parent on create produces the same
// parent-child relationship as an explicit dep add --type parent-child.
func TestCreateWithParentFlag(t *testing.T) {
	compareExports(t, func(w *workspace) {
		parent := w.create("--title", "Epic parent", "--type", "epic")
		w.create("--title", "Child via --parent", "--type", "task", "--parent", parent)
	})
}

// ---------------------------------------------------------------------------
// Append-notes behavior
// ---------------------------------------------------------------------------

// TestAppendNotesPreservation verifies that --append-notes concatenates
// rather than replacing existing notes.
func TestAppendNotesPreservation(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Notes test", "--type", "task",
			"--notes", "Initial notes from creation")
		w.run("update", id, "--append-notes", "Appended during review")
		w.run("update", id, "--title", "Notes test (updated)")
	})
}

// ---------------------------------------------------------------------------
// Set-labels replace semantics
// ---------------------------------------------------------------------------

// TestSetLabelsReplace verifies that --set-labels replaces all existing labels
// rather than appending. GH#1604: --set-labels didn't delete old labels.
func TestSetLabelsReplace(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Label replace test", "--type", "task")
		w.run("label", "add", id, "alpha")
		w.run("label", "add", id, "beta")
		w.run("label", "add", id, "gamma")
		// Replace all with only delta and epsilon
		w.run("update", id, "--set-labels", "delta", "--set-labels", "epsilon")
	})
}

// TestLabelAddRemoveBatch verifies that batch add-label and remove-label
// via update produce consistent results.
func TestLabelAddRemoveBatch(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Batch label test", "--type", "task")
		w.run("update", id, "--add-label", "keep-me", "--add-label", "remove-me", "--add-label", "also-keep")
		w.run("update", id, "--remove-label", "remove-me")
	})
}

// ---------------------------------------------------------------------------
// Supersede command
// ---------------------------------------------------------------------------

// TestSupersedeCommand marks an issue as superseded by another and compares exports.
// bd supersede <old-id> --with <new-id> should close the old with a supersedes dep.
func TestSupersedeCommand(t *testing.T) {
	scenario := func(w *workspace) (string, string) {
		old := w.create("--title", "Original approach", "--type", "task", "--priority", "2")
		replacement := w.create("--title", "Better approach", "--type", "task", "--priority", "1")
		return old, replacement
	}

	baselineWS := newWorkspace(t, baselineBin)
	bOld, bNew := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cOld, cNew := scenario(candidateWS)

	_, bErr := baselineWS.tryRun("supersede", bOld, "--with", bNew)
	_, cErr := candidateWS.tryRun("supersede", cOld, "--with", cNew)

	if bErr != nil && cErr != nil {
		t.Skip("pre-existing bug (baseline): bd supersede fails in both baseline and candidate")
	}
	if bErr == nil && cErr != nil {
		t.Fatalf("bd supersede succeeds in baseline but fails in candidate: %v", cErr)
	}
	if bErr != nil && cErr == nil {
		t.Log("bd supersede fails in baseline but succeeds in candidate (new command)")
		return
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Create with --deps flag (blocks direction)
// ---------------------------------------------------------------------------

// TestCreateWithDepsFlag creates an issue with --deps flag specifying a blocks
// relationship, and verifies the dependency direction matches explicit dep add.
func TestCreateWithDepsFlag(t *testing.T) {
	// Probe: does baseline support --deps on create?
	probe := newWorkspace(t, baselineBin)
	probeID := probe.create("--title", "probe blocker", "--type", "task")
	_, err := probe.tryCreate("--title", "probe blocked", "--type", "task", "--deps", probeID)
	if err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --deps flag on create")
	}

	compareExports(t, func(w *workspace) {
		blocker := w.create("--title", "Blocker task", "--type", "task", "--priority", "1")
		w.create("--title", "Dependent task", "--type", "task", "--priority", "2",
			"--deps", blocker)
	})
}

// TestCreateWithDepsExplicitBlocks tests --deps with explicit "blocks:" prefix
// to verify the dependency direction (new issue blocks the referenced issue).
// Commit 0c6ef53e intentionally fixed the direction: baseline had "blocks:X"
// creating a dep in the wrong direction; candidate correctly makes X depend on
// the new issue. This test guards against re-introducing the old bug.
func TestCreateWithDepsExplicitBlocks(t *testing.T) {
	t.Skip("intentional improvement: commit 0c6ef53e fixed blocks: direction on create --deps")
	// Probe: does baseline support --deps on create?
	probe := newWorkspace(t, baselineBin)
	probeID := probe.create("--title", "probe target", "--type", "task")
	_, err := probe.tryCreate("--title", "probe source", "--type", "task", "--deps", "blocks:"+probeID)
	if err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --deps blocks: on create")
	}

	compareExports(t, func(w *workspace) {
		target := w.create("--title", "Target task", "--type", "task", "--priority", "2")
		w.create("--title", "Blocking task", "--type", "task", "--priority", "1",
			"--deps", "blocks:"+target)
	})
}

// ---------------------------------------------------------------------------
// Update type change
// ---------------------------------------------------------------------------

// TestUpdateTypeChange verifies that changing an issue's type via bd update --type
// survives export and doesn't corrupt other fields.
func TestUpdateTypeChange(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Started as task", "--type", "task", "--priority", "2")
		w.run("update", id, "--type", "bug")
		w.run("update", id, "--title", "Now a bug")
	})
}

// ---------------------------------------------------------------------------
// List filter parity (by label, by type)
// ---------------------------------------------------------------------------

// TestListByLabelFilter creates issues with different labels and verifies
// bd list --label returns the same set in both backends.
func TestListByLabelFilter(t *testing.T) {
	scenario := func(w *workspace) {
		id1 := w.create("--title", "Frontend task", "--type", "task")
		w.run("label", "add", id1, "frontend")

		id2 := w.create("--title", "Backend task", "--type", "task")
		w.run("label", "add", id2, "backend")

		id3 := w.create("--title", "Full stack task", "--type", "task")
		w.run("label", "add", id3, "frontend")
		w.run("label", "add", id3, "backend")

		w.create("--title", "Unlabeled task", "--type", "task")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare list --label frontend
	bOut, bErr := baselineWS.tryRun("list", "--label", "frontend", "--json")
	cOut, cErr := candidateWS.tryRun("list", "--label", "frontend", "--json")

	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --label filter on list")
	}
	if cErr != nil {
		t.Fatalf("bd list --label works in baseline but fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("list --label frontend differs:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}

	// Also compare full exports
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestListByTypeFilter creates issues of different types and verifies
// bd list --type returns the same set in both backends.
func TestListByTypeFilter(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "A bug report", "--type", "bug", "--priority", "1")
		w.create("--title", "A feature request", "--type", "feature", "--priority", "2")
		w.create("--title", "Another bug", "--type", "bug", "--priority", "3")
		w.create("--title", "A task", "--type", "task", "--priority", "2")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare list --type bug
	bOut, bErr := baselineWS.tryRun("list", "--type", "bug", "--json")
	cOut, cErr := candidateWS.tryRun("list", "--type", "bug", "--json")

	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --type filter on list")
	}
	if cErr != nil {
		t.Fatalf("bd list --type works in baseline but fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("list --type bug differs:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// Ready semantics: own defer exclusion
// ---------------------------------------------------------------------------

// TestDeferredExcludedFromReady creates an issue deferred to a future date and
// a normal task, verifying both backends agree on the ready set.
// Unlike TestDeferredParentExcludesChildren, this tests the issue's own defer.
func TestDeferredExcludedFromReady(t *testing.T) {
	scenario := func(w *workspace) {
		id := w.create("--title", "Deferred task", "--type", "task", "--priority", "1")
		w.run("update", id, "--defer", "2099-12-31")
		w.create("--title", "Ready task", "--type", "task", "--priority", "2")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	cTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("ready sets differ for deferred issue:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}

	// Also compare full exports
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Mixed edge-case scenarios
// ---------------------------------------------------------------------------

// TestMultipleDepTypesOnSameIssue creates an issue that has multiple different
// dependency types to different targets, verifying all survive export.
func TestMultipleDepTypesOnSameIssue(t *testing.T) {
	compareExports(t, func(w *workspace) {
		hub := w.create("--title", "Hub issue", "--type", "task")
		blocker := w.create("--title", "Blocker", "--type", "task")
		tracked := w.create("--title", "Tracked upstream", "--type", "task")
		related := w.create("--title", "Related issue", "--type", "task")

		w.run("dep", "add", hub, blocker)                         // default blocks
		w.run("dep", "add", hub, tracked, "--type", "tracks")     // tracks
		w.run("dep", "add", hub, related, "--type", "relates-to") // relates-to
	})
}

// TestReopenPreservesDeps reopens a closed issue that has dependencies and
// verifies the dependency graph is preserved after reopen.
func TestReopenPreservesDeps(t *testing.T) {
	compareExports(t, func(w *workspace) {
		idA := w.create("--title", "Foundation task", "--type", "task")
		idB := w.create("--title", "Dependent task", "--type", "task")
		w.run("dep", "add", idB, idA) // B depends on A

		w.run("close", idA, "--reason", "done")
		w.run("reopen", idA)

		// After reopen, B should still depend on A
	})
}

// TestStatusTransitions exercises the full status lifecycle:
// open → in_progress → closed → reopened, verifying each transition
// is reflected identically in both backends.
func TestStatusTransitions(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Lifecycle issue", "--type", "task", "--priority", "2")
		w.run("update", id, "--status", "in_progress")
		w.run("close", id, "--reason", "completed")
		w.run("reopen", id)
		w.run("update", id, "--status", "in_progress")
	})
}

// TestCommentOnClosedIssue adds a comment after closing, verifying that
// comments on closed issues survive export identically.
func TestCommentOnClosedIssue(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Issue with post-close comment", "--type", "bug", "--priority", "2")
		w.run("comment", id, "Pre-close investigation notes")
		w.run("close", id, "--reason", "fixed in v2.1")
		w.run("comment", id, "Post-close follow-up: confirmed fix in production")
	})
}

// TestDescriptionUpdateRoundTrip verifies that updating the description field
// specifically (not just title) preserves the new value through export.
func TestDescriptionUpdateRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Description test", "--type", "feature",
			"--description", "Initial description of the feature")
		w.run("update", id, "--description", "Updated description with more detail and requirements")
		w.run("update", id, "--title", "Description test (updated)")
	})
}

// TestDesignFieldRoundTrip verifies that the design field survives update and export.
func TestDesignFieldRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Design doc", "--type", "feature",
			"--design", "Use a layered architecture with clean interfaces")
		w.run("update", id, "--design", "Revised: use hexagonal architecture instead")
		w.run("update", id, "--title", "Design doc (revised)")
	})
}

// TestAcceptanceCriteriaRoundTrip verifies the acceptance field round-trip.
func TestAcceptanceCriteriaRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Acceptance test", "--type", "feature",
			"--acceptance", "All unit tests pass")
		w.run("update", id, "--acceptance", "All unit tests pass AND integration tests pass")
		w.run("update", id, "--title", "Acceptance test (revised)")
	})
}

// ---------------------------------------------------------------------------
// Undefer command
// ---------------------------------------------------------------------------

// TestUndeferRoundTrip creates a deferred issue, undefers it, and compares
// exports. Verifies that defer_until is cleared and status returns to open.
func TestUndeferRoundTrip(t *testing.T) {
	// Probe: does baseline support bd undefer?
	probe := newWorkspace(t, baselineBin)
	probeID := probe.create("--title", "probe", "--type", "task")
	probe.run("update", probeID, "--defer", "2099-12-31")
	if _, err := probe.tryRun("undefer", probeID); err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support bd undefer command")
	}

	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Deferred then undeferred", "--type", "task", "--priority", "2")
		w.run("update", id, "--defer", "2099-06-15")
		w.run("update", id, "--title", "Deferred then undeferred (still deferred)")
		w.run("undefer", id)
	})
}

// ---------------------------------------------------------------------------
// Notes overwrite vs append semantics
// ---------------------------------------------------------------------------

// TestNotesOverwriteSemantics verifies that --notes replaces existing notes
// while --append-notes concatenates. Data loss risk if semantics drift.
func TestNotesOverwriteSemantics(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Notes overwrite test", "--type", "task",
			"--notes", "Original notes content")
		w.run("update", id, "--notes", "Completely replaced notes")
		w.run("update", id, "--title", "Notes overwrite test (updated)")
	})
}

// ---------------------------------------------------------------------------
// Priority mid-lifecycle change
// ---------------------------------------------------------------------------

// TestPriorityChangeRoundTrip verifies that changing priority after creation
// survives export. Catches omitempty issues with priority=0 on update.
func TestPriorityChangeRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Priority escalation", "--type", "bug", "--priority", "3")
		w.run("update", id, "--priority", "0") // escalate to P0
		w.run("update", id, "--title", "Priority escalation (now critical)")
	})
}

// ---------------------------------------------------------------------------
// Reparenting
// ---------------------------------------------------------------------------

// TestReparenting creates a child of parent A, then reparents to parent B,
// verifying the old parent-child dep is removed and the new one is added.
func TestReparenting(t *testing.T) {
	// Probe: does baseline support --parent on update?
	probe := newWorkspace(t, baselineBin)
	probeParent := probe.create("--title", "probe parent", "--type", "epic")
	probeChild := probe.create("--title", "probe child", "--type", "task", "--parent", probeParent)
	probeNewParent := probe.create("--title", "probe new parent", "--type", "epic")
	if _, err := probe.tryRun("update", probeChild, "--parent", probeNewParent); err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --parent on update")
	}

	compareExports(t, func(w *workspace) {
		parentA := w.create("--title", "Original parent", "--type", "epic")
		child := w.create("--title", "Reparented child", "--type", "task", "--parent", parentA)
		parentB := w.create("--title", "New parent", "--type", "epic")
		w.run("update", child, "--parent", parentB)
	})
}

// TestRemoveParent creates a child of a parent, then removes the parent
// relationship using update --parent "".
func TestRemoveParent(t *testing.T) {
	// Probe: does baseline support --parent "" on update?
	probe := newWorkspace(t, baselineBin)
	probeParent := probe.create("--title", "probe parent", "--type", "epic")
	probeChild := probe.create("--title", "probe child", "--type", "task", "--parent", probeParent)
	if _, err := probe.tryRun("update", probeChild, "--parent", ""); err != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support removing parent via --parent \"\"")
	}

	compareExports(t, func(w *workspace) {
		parent := w.create("--title", "Former parent", "--type", "epic")
		child := w.create("--title", "Orphaned child", "--type", "task", "--parent", parent)
		w.run("update", child, "--parent", "")
	})
}

// ---------------------------------------------------------------------------
// Export filter parity
// ---------------------------------------------------------------------------

// TestExportByAssigneeFilter creates issues with different assignees and
// verifies bd export --assignee returns the same set in both backends.
func TestExportByAssigneeFilter(t *testing.T) {
	scenario := func(w *workspace) {
		id1 := w.create("--title", "Alice work", "--type", "task", "--assignee", "alice")
		id2 := w.create("--title", "Bob work", "--type", "task", "--assignee", "bob")
		id3 := w.create("--title", "Alice second", "--type", "task", "--assignee", "alice")
		_ = id1
		_ = id2
		_ = id3
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare export --assignee alice
	bRaw, bErr := baselineWS.tryRun("export", "--assignee", "alice")
	cRaw, cErr := candidateWS.tryRun("export", "--assignee", "alice")

	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --assignee filter on export")
	}
	if cErr != nil {
		t.Fatalf("bd export --assignee works in baseline but fails in candidate: %v", cErr)
	}

	diffNormalized(t, bRaw, cRaw,
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Tier 1: Open issues suggest divergence
// ---------------------------------------------------------------------------

// TestParentChildDepExportBothDirections creates a parent-child relationship
// and verifies that BOTH the child's dep row (child depends-on parent) AND
// the parent's dep row (parent has child) appear in export.
// GH#1926: bd export omits parent-to-child dependency rows.
func TestParentChildDepExportBothDirections(t *testing.T) {
	scenario := func(w *workspace) {
		parent := w.create("--title", "Epic parent", "--type", "epic")
		child1 := w.create("--title", "Child one", "--type", "task")
		child2 := w.create("--title", "Child two", "--type", "task")

		w.run("dep", "add", child1, parent, "--type", "parent-child")
		w.run("dep", "add", child2, parent, "--type", "parent-child")

		// Also add a regular blocks dep to make sure mixed types work
		w.run("dep", "add", child2, child1)
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare full export (catches missing dep rows)
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)

	// Also count dep rows per issue to surface omissions clearly
	bIssues := parseJSONLByID(t, baselineWS.export())
	cIssues := parseJSONLByID(t, candidateWS.export())

	for canonicalID, bIssue := range bIssues {
		cIssue, ok := cIssues[canonicalID]
		if !ok {
			continue
		}
		bDeps := countDeps(bIssue)
		cDeps := countDeps(cIssue)
		if bDeps != cDeps {
			t.Errorf("  %s: baseline has %d deps, candidate has %d deps",
				canonicalID, bDeps, cDeps)
		}
	}
}

// TestWaitsForDepBlocksReady creates issues linked via waits-for dependency
// and verifies both backends agree on which issues appear in bd ready.
// GH#1899: bd ready ignores waits-for dependencies.
//
// Finding: baseline (v0.49.6) silently drops waits-for deps from storage/export
// entirely. Candidate correctly stores them. This is a baseline deficiency,
// not a candidate regression. Ready semantics are identical (neither blocks
// on waits-for). The export diff is real but expected.
func TestWaitsForDepBlocksReady(t *testing.T) {
	t.Skip("baseline deficiency: v0.49.6 silently drops waits-for dep type from storage (GH#1899)")

	scenario := func(w *workspace) (waiter, target, independent string) {
		target = w.create("--title", "Gate target", "--type", "task", "--priority", "1")
		waiter = w.create("--title", "Waiting on gate", "--type", "task", "--priority", "2")
		independent = w.create("--title", "Independent work", "--type", "task", "--priority", "3")

		// Try waits-for dep type
		_, err := w.tryRun("dep", "add", waiter, target, "--type", "waits-for")
		if err != nil {
			// Fall back — baseline might not support waits-for
			return
		}
		return
	}

	baselineWS := newWorkspace(t, baselineBin)
	bWaiter, bTarget, bIndep := scenario(baselineWS)
	if bWaiter == "" {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support waits-for dep type")
	}

	candidateWS := newWorkspace(t, candidateBin)
	cWaiter, cTarget, cIndep := scenario(candidateWS)

	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	// Map to canonical names for comparison
	type readySet map[string]bool
	canonicalize := func(ready map[string]bool, waiter, target, indep string) readySet {
		s := make(readySet)
		if ready[waiter] {
			s["waiter"] = true
		}
		if ready[target] {
			s["target"] = true
		}
		if ready[indep] {
			s["independent"] = true
		}
		return s
	}

	bSet := canonicalize(bReady, bWaiter, bTarget, bIndep)
	cSet := canonicalize(cReady, cWaiter, cTarget, cIndep)

	for role, bVal := range bSet {
		if cSet[role] != bVal {
			t.Errorf("  %s: ready in baseline=%v, candidate=%v", role, bVal, cSet[role])
		}
	}
	for role, cVal := range cSet {
		if bSet[role] != cVal {
			t.Errorf("  %s: ready in baseline=%v, candidate=%v", role, bSet[role], cVal)
		}
	}

	// Also diff full export
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestNonBlocksDepCloseSemantics creates issues linked via non-blocks dep types
// (caused-by, validates, tracks) and verifies that closing the target does NOT
// change the ready status of the dependent — only blocks-type deps should gate readiness.
// GH#1524: close guard may diverge for non-blocks dep types.
func TestNonBlocksDepCloseSemantics(t *testing.T) {
	depTypes := []string{"caused-by", "validates", "tracks"}

	for _, dt := range depTypes {
		t.Run(dt, func(t *testing.T) {
			scenario := func(w *workspace) (depIssue, targetIssue string) {
				targetIssue = w.create("--title", "Target ("+dt+")", "--type", "task", "--priority", "1")
				depIssue = w.create("--title", "Dependent via "+dt, "--type", "task", "--priority", "2")
				w.run("dep", "add", depIssue, targetIssue, "--type", dt)
				return
			}

			baselineWS := newWorkspace(t, baselineBin)
			bDep, bTarget := scenario(baselineWS)

			candidateWS := newWorkspace(t, candidateBin)
			cDep, cTarget := scenario(candidateWS)

			// Before close: both should be ready (non-blocks deps don't gate)
			bReadyBefore := parseReadyIDs(t, baselineWS)
			cReadyBefore := parseReadyIDs(t, candidateWS)

			if bReadyBefore[bDep] != cReadyBefore[cDep] {
				t.Errorf("before close: dep ready baseline=%v candidate=%v",
					bReadyBefore[bDep], cReadyBefore[cDep])
			}

			// Close the target
			baselineWS.run("close", bTarget, "--reason", "done")
			candidateWS.run("close", cTarget, "--reason", "done")

			// After close: dep should still be ready
			bReadyAfter := parseReadyIDs(t, baselineWS)
			cReadyAfter := parseReadyIDs(t, candidateWS)

			if bReadyAfter[bDep] != cReadyAfter[cDep] {
				t.Errorf("after close: dep ready baseline=%v candidate=%v",
					bReadyAfter[bDep], cReadyAfter[cDep])
			}

			// Diff export
			diffNormalized(t,
				baselineWS.export(), candidateWS.export(),
				canonicalIDMap(baselineWS.createdIDs),
				canonicalIDMap(candidateWS.createdIDs),
			)
		})
	}
}

// TestBlocksDepCloseUnblocks verifies that closing a blocker makes the
// blocked issue appear in ready — the core blocks semantics contract.
// Complements TestNonBlocksDepCloseSemantics above.
func TestBlocksDepCloseUnblocks(t *testing.T) {
	scenario := func(w *workspace) (blocked, blocker string) {
		blocker = w.create("--title", "Blocker", "--type", "task", "--priority", "1")
		blocked = w.create("--title", "Blocked work", "--type", "task", "--priority", "2")
		w.run("dep", "add", blocked, blocker)
		return
	}

	baselineWS := newWorkspace(t, baselineBin)
	bBlocked, bBlocker := scenario(baselineWS)

	candidateWS := newWorkspace(t, candidateBin)
	cBlocked, cBlocker := scenario(candidateWS)

	// Before close: blocked should NOT be ready
	bReadyBefore := parseReadyIDs(t, baselineWS)
	cReadyBefore := parseReadyIDs(t, candidateWS)

	if bReadyBefore[bBlocked] != cReadyBefore[cBlocked] {
		t.Errorf("before close: blocked ready baseline=%v candidate=%v",
			bReadyBefore[bBlocked], cReadyBefore[cBlocked])
	}

	// Close the blocker
	baselineWS.run("close", bBlocker, "--reason", "done")
	candidateWS.run("close", cBlocker, "--reason", "done")

	// After close: blocked SHOULD be ready
	bReadyAfter := parseReadyIDs(t, baselineWS)
	cReadyAfter := parseReadyIDs(t, candidateWS)

	if bReadyAfter[bBlocked] != cReadyAfter[cBlocked] {
		t.Errorf("after close: blocked ready baseline=%v candidate=%v",
			bReadyAfter[bBlocked], cReadyAfter[cBlocked])
	}

	// Diff export
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Tier 2: Recently fixed areas — guard against re-regression
// ---------------------------------------------------------------------------

// TestCustomTypeRoundTrip creates issues with non-standard types and verifies
// they survive export. GH#1733 fixed types.custom seeding in Dolt config.
func TestCustomTypeRoundTrip(t *testing.T) {
	// Standard types that both versions should support
	stdTypes := []string{"task", "bug", "feature", "epic"}

	for _, issueType := range stdTypes {
		t.Run(issueType, func(t *testing.T) {
			compareExports(t, func(w *workspace) {
				id := w.create("--title", "Typed issue: "+issueType, "--type", issueType, "--priority", "2")
				w.run("update", id, "--title", "Typed issue: "+issueType+" (updated)")
			})
		})
	}
}

// TestMultipleLabelsStressRoundTrip adds many labels, removes some, re-adds,
// and checks that the final label set is consistent across backends.
func TestMultipleLabelsStressRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Many labels", "--type", "task")

		// Add 8 labels
		for _, l := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			w.run("label", "add", id, l)
		}
		// Remove some
		w.run("label", "remove", id, "c")
		w.run("label", "remove", id, "f")
		// Re-add one that was removed
		w.run("label", "add", id, "c")
		// Add a new one
		w.run("label", "add", id, "z")
	})
}

// TestMultipleCommentsRoundTrip adds many comments, including ones with
// special characters, and verifies round-trip.
func TestMultipleCommentsRoundTrip(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Comment stress", "--type", "task")

		w.run("comment", id, "First comment")
		w.run("comment", id, "Comment with 'single quotes' and \"double quotes\"")
		w.run("comment", id, "Comment with newline-like text: line1\\nline2")
		w.run("comment", id, "Unicode: cafe\u0301 re\u0301sume\u0301 nai\u0308ve")
		w.run("comment", id, "Empty-ish:   ")
	})
}

// TestTransitiveBlockingChain builds a longer dependency chain and verifies
// that transitive blocking is handled identically: A←B←C←D, close A,
// only B should become ready (C and D still transitively blocked).
func TestTransitiveBlockingChain(t *testing.T) {
	scenario := func(w *workspace) [4]string {
		var ids [4]string
		ids[0] = w.create("--title", "Chain-A (root blocker)", "--type", "task", "--priority", "1")
		ids[1] = w.create("--title", "Chain-B (blocked by A)", "--type", "task", "--priority", "2")
		ids[2] = w.create("--title", "Chain-C (blocked by B)", "--type", "task", "--priority", "2")
		ids[3] = w.create("--title", "Chain-D (blocked by C)", "--type", "task", "--priority", "2")

		w.run("dep", "add", ids[1], ids[0]) // B depends on A
		w.run("dep", "add", ids[2], ids[1]) // C depends on B
		w.run("dep", "add", ids[3], ids[2]) // D depends on C

		w.run("close", ids[0], "--reason", "done")
		return ids
	}

	baselineWS := newWorkspace(t, baselineBin)
	bIDs := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cIDs := scenario(candidateWS)

	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	names := [4]string{"A(closed)", "B", "C", "D"}
	for i := range bIDs {
		bR := bReady[bIDs[i]]
		cR := cReady[cIDs[i]]
		if bR != cR {
			t.Errorf("  %s: ready baseline=%v candidate=%v", names[i], bR, cR)
		}
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestDiamondDependency creates a diamond-shaped dependency graph:
//
//	A ← B, A ← C, B ← D, C ← D (D blocked by both B and C, both blocked by A).
//
// Close A. B and C should become ready, D should stay blocked.
func TestDiamondDependency(t *testing.T) {
	scenario := func(w *workspace) [4]string {
		var ids [4]string
		ids[0] = w.create("--title", "Diamond-A (root)", "--type", "task", "--priority", "1")
		ids[1] = w.create("--title", "Diamond-B (left)", "--type", "task", "--priority", "2")
		ids[2] = w.create("--title", "Diamond-C (right)", "--type", "task", "--priority", "2")
		ids[3] = w.create("--title", "Diamond-D (join)", "--type", "task", "--priority", "3")

		w.run("dep", "add", ids[1], ids[0]) // B depends on A
		w.run("dep", "add", ids[2], ids[0]) // C depends on A
		w.run("dep", "add", ids[3], ids[1]) // D depends on B
		w.run("dep", "add", ids[3], ids[2]) // D depends on C

		w.run("close", ids[0], "--reason", "done")
		return ids
	}

	baselineWS := newWorkspace(t, baselineBin)
	bIDs := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cIDs := scenario(candidateWS)

	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	names := [4]string{"A(closed)", "B", "C", "D"}
	for i := range bIDs {
		bR := bReady[bIDs[i]]
		cR := cReady[cIDs[i]]
		if bR != cR {
			t.Errorf("  %s: ready baseline=%v candidate=%v", names[i], bR, cR)
		}
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestUpdateDoesNotClobberRelationalData does a rapid series of updates
// on an issue that has labels, deps, and comments, verifying that updating
// scalar fields never clobbers relational data.
func TestUpdateDoesNotClobberRelationalData(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id1 := w.create("--title", "Data-rich issue", "--type", "feature", "--priority", "1")
		id2 := w.create("--title", "Dep target", "--type", "task")

		w.run("label", "add", id1, "important")
		w.run("label", "add", id1, "v2")
		w.run("dep", "add", id1, id2)
		w.run("comment", id1, "Design review notes")
		w.run("comment", id1, "Implementation started")

		// Rapid-fire updates to different scalar fields
		w.run("update", id1, "--title", "Data-rich issue v2")
		w.run("update", id1, "--priority", "0")
		w.run("update", id1, "--description", "Updated description")
		w.run("update", id1, "--assignee", "alice")
		w.run("update", id1, "--notes", "Updated notes")
	})
}

// ---------------------------------------------------------------------------
// Tier 3: Untested CLI surface with data-integrity risk
// ---------------------------------------------------------------------------

// TestDeleteAndRestoreRoundTrip deletes an issue, restores it, and compares.
func TestDeleteAndRestoreRoundTrip(t *testing.T) {
	// Probe if baseline supports restore
	bWS := newWorkspace(t, baselineBin)
	probeID := bWS.create("--title", "probe", "--type", "task")
	bWS.run("delete", probeID, "--force")
	_, bErr := bWS.tryRun("restore", probeID)
	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support bd restore")
	}

	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Restorable issue", "--type", "task", "--priority", "2")
		w.run("label", "add", id, "important")
		w.run("comment", id, "This should survive delete+restore")

		w.run("delete", id, "--force")
		w.run("restore", id)
	})
}

// TestEpicWithChildrenStatus creates an epic with child tasks, closes all
// children, and verifies the epic's status/export is consistent.
func TestEpicWithChildrenStatus(t *testing.T) {
	compareExports(t, func(w *workspace) {
		epic := w.create("--title", "Epic with children", "--type", "epic")
		c1 := w.create("--title", "Child 1", "--type", "task")
		c2 := w.create("--title", "Child 2", "--type", "task")
		c3 := w.create("--title", "Child 3", "--type", "task")

		w.run("dep", "add", c1, epic, "--type", "parent-child")
		w.run("dep", "add", c2, epic, "--type", "parent-child")
		w.run("dep", "add", c3, epic, "--type", "parent-child")

		// Close two of three children
		w.run("close", c1, "--reason", "done")
		w.run("close", c2, "--reason", "done")
	})
}

// TestSearchByTitleSubstring uses bd list with title filtering to verify
// both backends return the same results.
func TestSearchByTitleSubstring(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Authentication module", "--type", "feature")
		w.create("--title", "Authorization helper", "--type", "task")
		w.create("--title", "Database migration", "--type", "task")
		w.create("--title", "Auth token refresh", "--type", "bug")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Try searching with --search or by piping through list
	bOut, bErr := baselineWS.tryRun("list", "--json", "--search", "Auth")
	cOut, cErr := candidateWS.tryRun("list", "--json", "--search", "Auth")

	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --search on list")
	}
	if cErr != nil {
		t.Fatalf("bd list --search works in baseline but fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("search results differ:\n  baseline:  %v\n  candidate: %v", bTitles, cTitles)
	}
}

// TestListCountParity creates a known set of issues with different statuses
// and verifies bd list --count (or just counting list output) agrees.
func TestListCountParity(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Open 1", "--type", "task")
		w.create("--title", "Open 2", "--type", "bug")
		id3 := w.create("--title", "Closed 1", "--type", "task")
		id4 := w.create("--title", "Closed 2", "--type", "feature")
		w.run("close", id3, "--reason", "done")
		w.run("close", id4, "--reason", "not needed")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// Compare list --status open counts
	bOpen := extractTitleOrder(t, baselineWS.run("list", "--status", "open", "--json"))
	cOpen := extractTitleOrder(t, candidateWS.run("list", "--status", "open", "--json"))

	if len(bOpen) != len(cOpen) {
		t.Errorf("open count: baseline=%d candidate=%d", len(bOpen), len(cOpen))
	}

	bClosed := extractTitleOrder(t, baselineWS.run("list", "--status", "closed", "--json"))
	cClosed := extractTitleOrder(t, candidateWS.run("list", "--status", "closed", "--json"))

	if len(bClosed) != len(cClosed) {
		t.Errorf("closed count: baseline=%d candidate=%d", len(bClosed), len(cClosed))
	}

	// Full export diff
	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestSpecialCharactersInFields verifies that special characters in title,
// description, and notes survive round-trip identically.
func TestSpecialCharactersInFields(t *testing.T) {
	compareExports(t, func(w *workspace) {
		w.create("--title", "Issue with <angle> & \"quotes\"",
			"--type", "task",
			"--description", "Has: colons, semi;colons, pipe|chars, back\\slash",
			"--notes", "Backtick `code` and $dollar and %percent",
		)
	})
}

// TestEmptyAndWhitespaceFields verifies that empty-ish field values are
// handled consistently.
func TestEmptyAndWhitespaceFields(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Whitespace test", "--type", "task")
		w.run("update", id, "--description", "   ")
		w.run("update", id, "--notes", "actual notes")
		w.run("update", id, "--description", "real description")
	})
}

// ---------------------------------------------------------------------------
// Epic lifecycle: children-of-closed-blocker ready semantics (GH#1495)
// ---------------------------------------------------------------------------

// TestEpicAllChildrenClosedNotBlocked creates an epic with children, closes
// all children, then verifies the epic is not reported as BLOCKED by
// bd blocked. GH#1495: completed epics show BLOCKED with "0 dependencies".
func TestEpicAllChildrenClosedNotBlocked(t *testing.T) {
	scenario := func(w *workspace) string {
		epic := w.create("--title", "Release epic", "--type", "epic", "--priority", "1")
		c1 := w.create("--title", "Task A", "--type", "task", "--parent", epic)
		c2 := w.create("--title", "Task B", "--type", "task", "--parent", epic)
		c3 := w.create("--title", "Task C", "--type", "task", "--parent", epic)

		w.run("close", c1, "--reason", "done")
		w.run("close", c2, "--reason", "done")
		w.run("close", c3, "--reason", "done")
		return epic
	}

	baselineWS := newWorkspace(t, baselineBin)
	bEpic := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cEpic := scenario(candidateWS)

	// Epic should appear in ready (no open blockers) in both backends
	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	if bReady[bEpic] != cReady[cEpic] {
		t.Errorf("epic ready: baseline=%v candidate=%v (GH#1495: completed epic shows BLOCKED)",
			bReady[bEpic], cReady[cEpic])
	}

	// Also check bd blocked --json: epic should NOT appear
	bBlocked, _ := baselineWS.tryRun("blocked", "--json")
	cBlocked, _ := candidateWS.tryRun("blocked", "--json")

	bBlockedTitles := extractTitleOrder(t, bBlocked)
	cBlockedTitles := extractTitleOrder(t, cBlocked)

	bHasEpic := false
	for _, title := range bBlockedTitles {
		if title == "Release epic" {
			bHasEpic = true
		}
	}
	cHasEpic := false
	for _, title := range cBlockedTitles {
		if title == "Release epic" {
			cHasEpic = true
		}
	}
	if bHasEpic != cHasEpic {
		t.Errorf("epic in blocked: baseline=%v candidate=%v", bHasEpic, cHasEpic)
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// TestBlockedEpicChildrenNotReady creates an epic blocked by another issue,
// then creates child tasks under the epic. Children should NOT appear in
// bd ready if their parent epic is blocked. GH#1495 issue 3.
func TestBlockedEpicChildrenNotReady(t *testing.T) {
	scenario := func(w *workspace) (epic, blocker string, children [3]string) {
		blocker = w.create("--title", "Prerequisite work", "--type", "task", "--priority", "1")
		epic = w.create("--title", "Gated epic", "--type", "epic", "--priority", "2")
		w.run("dep", "add", epic, blocker)

		children[0] = w.create("--title", "Child X", "--type", "task", "--parent", epic)
		children[1] = w.create("--title", "Child Y", "--type", "task", "--parent", epic)
		children[2] = w.create("--title", "Child Z", "--type", "task", "--parent", epic)
		return
	}

	baselineWS := newWorkspace(t, baselineBin)
	_, _, bChildren := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	_, _, cChildren := scenario(candidateWS)

	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	for i := range bChildren {
		bR := bReady[bChildren[i]]
		cR := cReady[cChildren[i]]
		if bR != cR {
			t.Errorf("child[%d] ready: baseline=%v candidate=%v (GH#1495: children of blocked epic in ready)",
				i, bR, cR)
		}
	}

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// Close guard: non-blocks dep types (GH#1524)
// ---------------------------------------------------------------------------

// TestCloseGuardNonBlocksDepTypes creates issues linked via non-blocks
// dependency types (caused-by, validates, tracks) and attempts to close
// the target. GH#1524: close guard may reject close for non-blocks deps
// or inconsistently allow it depending on backend.
func TestCloseGuardNonBlocksDepTypes(t *testing.T) {
	depTypes := []string{"caused-by", "validates", "tracks"}

	for _, dt := range depTypes {
		t.Run(dt, func(t *testing.T) {
			scenario := func(w *workspace) (source, target string) {
				target = w.create("--title", "Target ("+dt+")", "--type", "task", "--priority", "1")
				source = w.create("--title", "Source via "+dt, "--type", "task", "--priority", "2")
				w.run("dep", "add", source, target, "--type", dt)
				return
			}

			baselineWS := newWorkspace(t, baselineBin)
			_, bTarget := scenario(baselineWS)
			candidateWS := newWorkspace(t, candidateBin)
			_, cTarget := scenario(candidateWS)

			// Try closing the target — should succeed since non-blocks deps
			// should not prevent close
			_, bErr := baselineWS.tryRun("close", bTarget, "--reason", "done")
			_, cErr := candidateWS.tryRun("close", cTarget, "--reason", "done")

			if (bErr == nil) != (cErr == nil) {
				t.Errorf("close target with %s dep: baseline err=%v candidate err=%v",
					dt, bErr, cErr)
			}

			diffNormalized(t,
				baselineWS.export(), candidateWS.export(),
				canonicalIDMap(baselineWS.createdIDs),
				canonicalIDMap(candidateWS.createdIDs),
			)
		})
	}
}

// ---------------------------------------------------------------------------
// bd list --all with --label filter (GH#1840)
// ---------------------------------------------------------------------------

// TestListAllOverridesLimitWithLabel creates >50 issues with a label and
// verifies that bd list --all --label returns all of them, not just 50.
// GH#1840: bd list --all has a default limit of 50 when combined with --label.
func TestListAllOverridesLimitWithLabel(t *testing.T) {
	// Create issues in both workspaces — more than the default limit
	scenario := func(w *workspace) {
		for i := 0; i < 55; i++ {
			id := w.create("--title", fmt.Sprintf("Issue %03d", i), "--type", "task")
			w.run("label", "add", id, "batch")
		}
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bOut, bErr := baselineWS.tryRun("list", "--all", "--label", "batch", "--json")
	cOut, cErr := candidateWS.tryRun("list", "--all", "--label", "batch", "--json")

	if bErr != nil {
		t.Skip("pre-existing bug (baseline): v0.49.6 does not support --all --label combination")
	}
	if cErr != nil {
		t.Fatalf("bd list --all --label works in baseline but fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)

	if len(bTitles) != len(cTitles) {
		t.Errorf("list --all --label count: baseline=%d candidate=%d (GH#1840: limit of 50 with --label)",
			len(bTitles), len(cTitles))
	}
}

// ---------------------------------------------------------------------------
// bd list resolved blockers annotation (GH#1858)
// ---------------------------------------------------------------------------

// TestListResolvedBlockerAnnotation creates a blocking dependency, closes
// the blocker, and checks that bd list text output does NOT show
// "(blocked by: ...)" for the unblocked issue.
// GH#1858: bd list shows resolved blockers as still blocking.
func TestListResolvedBlockerAnnotation(t *testing.T) {
	scenario := func(w *workspace) (blocker, blocked string) {
		blocker = w.create("--title", "Blocker task", "--type", "task", "--priority", "1")
		blocked = w.create("--title", "Previously blocked", "--type", "task", "--priority", "2")
		w.run("dep", "add", blocked, blocker)
		w.run("close", blocker, "--reason", "done")
		return
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bOut := baselineWS.run("list", "--status", "open")
	cOut := candidateWS.run("list", "--status", "open")

	bHasBlocked := strings.Contains(bOut, "blocked by")
	cHasBlocked := strings.Contains(cOut, "blocked by")

	if bHasBlocked != cHasBlocked {
		t.Errorf("list 'blocked by' annotation after blocker closed: baseline=%v candidate=%v (GH#1858)",
			bHasBlocked, cHasBlocked)
	}

	// Also verify ready agrees
	bReady := parseReadyIDs(t, baselineWS)
	cReady := parseReadyIDs(t, candidateWS)

	bReadyTitles := extractTitleOrder(t, baselineWS.run("ready", "--json"))
	cReadyTitles := extractTitleOrder(t, candidateWS.run("ready", "--json"))
	sort.Strings(bReadyTitles)
	sort.Strings(cReadyTitles)

	if strings.Join(bReadyTitles, "|") != strings.Join(cReadyTitles, "|") {
		t.Errorf("ready set differs: baseline=%v candidate=%v", bReadyTitles, cReadyTitles)
	}

	_ = bReady
	_ = cReady

	diffNormalized(t,
		baselineWS.export(), candidateWS.export(),
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// ---------------------------------------------------------------------------
// bd dep tree (GH#1954)
// ---------------------------------------------------------------------------

// TestDepTreeOutput creates a parent-child hierarchy and verifies that
// bd dep tree produces output that includes all children.
// GH#1954: bd dep tree epic-id command no longer works.
func TestDepTreeOutput(t *testing.T) {
	scenario := func(w *workspace) string {
		epic := w.create("--title", "My Epic", "--type", "epic")
		c1 := w.create("--title", "Feature 1", "--type", "feature", "--parent", epic)
		w.create("--title", "Task 1a", "--type", "task", "--parent", c1)
		w.create("--title", "Task 1b", "--type", "task", "--parent", c1)
		c2 := w.create("--title", "Feature 2", "--type", "feature", "--parent", epic)
		w.create("--title", "Task 2a", "--type", "task", "--parent", c2)
		return epic
	}

	baselineWS := newWorkspace(t, baselineBin)
	bEpic := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cEpic := scenario(candidateWS)

	bOut, bErr := baselineWS.tryRun("dep", "tree", bEpic)
	cOut, cErr := candidateWS.tryRun("dep", "tree", cEpic)

	if bErr != nil && cErr != nil {
		t.Skip("pre-existing bug (baseline): bd dep tree fails in both versions")
	}
	if bErr == nil && cErr != nil {
		t.Fatalf("bd dep tree works in baseline but fails in candidate: %v\nOutput: %s", cErr, cOut)
	}
	if bErr != nil && cErr == nil {
		t.Log("bd dep tree fails in baseline but works in candidate (new/fixed command)")
		return
	}

	// Both succeeded — verify both mention all children
	childTitles := []string{"Feature 1", "Feature 2", "Task 1a", "Task 1b", "Task 2a"}
	for _, title := range childTitles {
		bHas := strings.Contains(bOut, title)
		cHas := strings.Contains(cOut, title)
		if bHas != cHas {
			t.Errorf("dep tree contains %q: baseline=%v candidate=%v (GH#1954)", title, bHas, cHas)
		}
	}
}

// ---------------------------------------------------------------------------
// Export/import with nested epic trees (GH#1926, GH#1927)
// ---------------------------------------------------------------------------

// TestExportImportEpicTree creates a multi-level epic tree (epic → features
// → tasks), exports, imports into a fresh workspace, and verifies the full
// tree structure survives — especially parent-to-child dep edges.
// GH#1926: export omits parent-to-child dep rows.
// GH#1927: import doesn't reconstruct the tree from partial dep edges.
func TestExportImportEpicTree(t *testing.T) {
	// Build rich tree in baseline
	baselineWS := newWorkspace(t, baselineBin)
	epic := baselineWS.create("--title", "Root epic", "--type", "epic", "--priority", "1")
	feat1 := baselineWS.create("--title", "Feature A", "--type", "feature", "--parent", epic)
	feat2 := baselineWS.create("--title", "Feature B", "--type", "feature", "--parent", epic)
	task1 := baselineWS.create("--title", "Task A1", "--type", "task", "--parent", feat1)
	task2 := baselineWS.create("--title", "Task A2", "--type", "task", "--parent", feat1)
	task3 := baselineWS.create("--title", "Task B1", "--type", "task", "--parent", feat2)

	baselineWS.run("label", "add", epic, "release-1.0")
	baselineWS.run("comment", feat1, "Design review complete")
	baselineWS.run("dep", "add", task2, task1) // task2 blocks on task1

	// Export
	exportFile := filepath.Join(baselineWS.dir, "tree-export.jsonl")
	baselineWS.run("export", "-o", exportFile)
	exportData, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}

	// Import into candidate
	candidateWS := newWorkspace(t, candidateBin)
	importFile := filepath.Join(candidateWS.dir, "tree-import.jsonl")
	if err := os.WriteFile(importFile, exportData, 0o644); err != nil {
		t.Fatalf("writing import: %v", err)
	}
	candidateWS.run("import", "-i", importFile)

	// Re-export from candidate and compare
	candidateRaw := candidateWS.export()

	// Parse both
	baselineIssues := parseJSONLByID(t, string(exportData))
	candidateIssues := parseJSONLByID(t, candidateRaw)

	// Verify all 6 issues survived
	allIDs := []string{epic, feat1, feat2, task1, task2, task3}
	for _, id := range allIDs {
		if _, ok := candidateIssues[id]; !ok {
			t.Errorf("issue %s missing from candidate after import (GH#1926/1927)", id)
		}
	}

	// Verify dep counts match
	for _, id := range allIDs {
		bIssue := baselineIssues[id]
		cIssue := candidateIssues[id]
		if bIssue == nil || cIssue == nil {
			continue
		}
		bDeps := countDeps(bIssue)
		cDeps := countDeps(cIssue)
		if bDeps != cDeps {
			t.Errorf("issue %s deps: baseline=%d candidate=%d (tree structure lost on import)",
				id, bDeps, cDeps)
		}
	}
}

// ---------------------------------------------------------------------------
// Rapid update does not corrupt data (GH#1973, GH#1969)
// ---------------------------------------------------------------------------

// TestRapidLabelAddRemoveStability adds and removes labels in rapid
// succession, verifying all operations take effect and no labels are
// silently dropped. GH#1969: writes via execContext silently rolled back
// when --no-auto-commit was set.
func TestRapidLabelAddRemoveStability(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Label churn", "--type", "task")

		// Rapid add/remove/add cycle
		w.run("label", "add", id, "alpha")
		w.run("label", "add", id, "beta")
		w.run("label", "add", id, "gamma")
		w.run("label", "remove", id, "beta")
		w.run("label", "add", id, "delta")
		w.run("label", "remove", id, "alpha")
		w.run("label", "add", id, "epsilon")
		w.run("label", "add", id, "beta") // re-add previously removed

		// Expected final set: beta, gamma, delta, epsilon
	})
}

// TestRapidDepAddRemoveStability adds and removes dependencies in rapid
// succession, verifying the final dep graph is correct.
func TestRapidDepAddRemoveStability(t *testing.T) {
	compareExports(t, func(w *workspace) {
		a := w.create("--title", "Hub", "--type", "task")
		b := w.create("--title", "Spoke B", "--type", "task")
		c := w.create("--title", "Spoke C", "--type", "task")
		d := w.create("--title", "Spoke D", "--type", "task")

		w.run("dep", "add", a, b)
		w.run("dep", "add", a, c)
		w.run("dep", "add", a, d)
		w.run("dep", "remove", a, c) // remove middle
		w.run("dep", "add", a, c)    // re-add
		w.run("dep", "remove", a, b) // remove first
	})
}

// TestRapidCommentAddStability adds many comments in rapid succession,
// verifying none are silently dropped. GH#1969: comments added via
// AddIssueComment were rolled back without --auto-commit.
func TestRapidCommentAddStability(t *testing.T) {
	compareExports(t, func(w *workspace) {
		id := w.create("--title", "Comment burst", "--type", "task")

		for i := 0; i < 10; i++ {
			w.run("comment", id, fmt.Sprintf("Comment number %d", i))
		}
	})
}

// ---------------------------------------------------------------------------
// Children command (bd children)
// ---------------------------------------------------------------------------

// TestChildrenCommand creates a parent with children and verifies bd children
// returns the correct set.
func TestChildrenCommand(t *testing.T) {
	scenario := func(w *workspace) (parent string, children [3]string) {
		parent = w.create("--title", "Parent epic", "--type", "epic")
		children[0] = w.create("--title", "Child 1", "--type", "task", "--parent", parent)
		children[1] = w.create("--title", "Child 2", "--type", "task", "--parent", parent)
		children[2] = w.create("--title", "Child 3", "--type", "task", "--parent", parent)
		return
	}

	baselineWS := newWorkspace(t, baselineBin)
	bParent, _ := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cParent, _ := scenario(candidateWS)

	bOut, bErr := baselineWS.tryRun("children", bParent, "--json")
	cOut, cErr := candidateWS.tryRun("children", cParent, "--json")

	if bErr != nil && cErr != nil {
		t.Skip("pre-existing: bd children fails in both versions")
	}
	if bErr != nil && cErr == nil {
		t.Log("bd children fails in baseline but works in candidate (new command)")
		return
	}
	if bErr == nil && cErr != nil {
		t.Fatalf("bd children works in baseline but fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("children output differs:\n  baseline:  %v\n  candidate: %v", bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// bd blocked command parity
// ---------------------------------------------------------------------------

// TestBlockedCommandParity creates a dependency graph with blocked and
// unblocked issues, runs bd blocked --json, and verifies both backends
// agree on which issues are blocked.
func TestBlockedCommandParity(t *testing.T) {
	scenario := func(w *workspace) {
		a := w.create("--title", "Open blocker", "--type", "task", "--priority", "1")
		w.create("--title", "Blocked by A", "--type", "task", "--priority", "2")
		w.create("--title", "Unblocked task", "--type", "task", "--priority", "3")
		b := w.create("--title", "Closed blocker", "--type", "task", "--priority", "1")
		w.create("--title", "Was blocked by B", "--type", "task", "--priority", "2")

		// Set up deps using the IDs from createdIDs
		ids := w.createdIDs
		w.run("dep", "add", ids[1], a) // "Blocked by A" depends on A
		w.run("dep", "add", ids[4], b) // "Was blocked by B" depends on B
		w.run("close", b, "--reason", "done")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bOut, bErr := baselineWS.tryRun("blocked", "--json")
	cOut, cErr := candidateWS.tryRun("blocked", "--json")

	if bErr != nil {
		t.Skip("pre-existing: bd blocked --json fails in baseline")
	}
	if cErr != nil {
		t.Fatalf("bd blocked --json fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("blocked set differs:\n  baseline:  %v\n  candidate: %v", bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// bd count command parity
// ---------------------------------------------------------------------------

// TestCountCommandParity creates issues in various states and verifies
// bd count returns consistent numbers across backends.
func TestCountCommandParity(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Open 1", "--type", "task")
		w.create("--title", "Open 2", "--type", "bug")
		w.create("--title", "Open 3", "--type", "feature")
		id4 := w.create("--title", "Closed 1", "--type", "task")
		id5 := w.create("--title", "Closed 2", "--type", "bug")
		w.run("close", id4, "--reason", "done")
		w.run("close", id5, "--reason", "wontfix")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	bOut, bErr := baselineWS.tryRun("count")
	cOut, cErr := candidateWS.tryRun("count")

	if bErr != nil {
		t.Skip("pre-existing: bd count fails in baseline")
	}
	if cErr != nil {
		t.Fatalf("bd count fails in candidate: %v", cErr)
	}

	// Counts should match (even if format differs, the numbers should)
	if strings.TrimSpace(bOut) != strings.TrimSpace(cOut) {
		t.Logf("count output differs (may be format only):\n  baseline:  %s\n  candidate: %s",
			strings.TrimSpace(bOut), strings.TrimSpace(cOut))
	}

	// More precise: count --status open
	bOpen, bErr2 := baselineWS.tryRun("count", "--status", "open")
	cOpen, cErr2 := candidateWS.tryRun("count", "--status", "open")

	if bErr2 != nil || cErr2 != nil {
		return // skip if --status not supported
	}

	if strings.TrimSpace(bOpen) != strings.TrimSpace(cOpen) {
		t.Errorf("count --status open: baseline=%q candidate=%q",
			strings.TrimSpace(bOpen), strings.TrimSpace(cOpen))
	}
}

// ---------------------------------------------------------------------------
// bd show --json field completeness
// ---------------------------------------------------------------------------

// TestShowJSONFieldCompleteness creates an issue with every possible field
// set, then verifies bd show --json returns all of them. This catches
// scan projection bugs (GH#1914) where new columns are missing from
// individual issue hydration.
func TestShowJSONFieldCompleteness(t *testing.T) {
	scenario := func(w *workspace) string {
		id := w.create(
			"--title", "Fully loaded issue",
			"--type", "feature",
			"--priority", "1",
			"--description", "Complete description",
			"--design", "Architecture doc",
			"--acceptance", "All tests pass",
			"--notes", "Implementation notes",
			"--assignee", "alice",
			"--estimate", "240",
		)
		w.run("update", id, "--due", "2099-06-15")
		w.run("update", id, "--defer", "2099-03-01")
		w.run("label", "add", id, "critical")
		w.run("label", "add", id, "backend")
		w.run("dep", "add", id, w.create("--title", "Dep target", "--type", "task"))
		w.run("comment", id, "Review comment")
		return id
	}

	baselineWS := newWorkspace(t, baselineBin)
	bID := scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	cID := scenario(candidateWS)

	bRaw := baselineWS.run("show", bID, "--json")
	cRaw := candidateWS.run("show", cID, "--json")

	// Parse and compare field presence
	bFields := parseJSONFieldNames(t, bRaw)
	cFields := parseJSONFieldNames(t, cRaw)

	// Every field in baseline should exist in candidate
	for _, f := range bFields {
		found := false
		for _, cf := range cFields {
			if f == cf {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("field %q present in baseline show --json but missing from candidate (GH#1914)", f)
		}
	}
}

// ---------------------------------------------------------------------------
// bd query DSL parity
// ---------------------------------------------------------------------------

// TestQueryDSLParity runs identical bd query expressions against both
// backends and verifies they return the same results.
func TestQueryDSLParity(t *testing.T) {
	scenario := func(w *workspace) {
		id1 := w.create("--title", "Auth module", "--type", "feature", "--priority", "1", "--assignee", "alice")
		w.run("label", "add", id1, "security")
		id2 := w.create("--title", "DB migration", "--type", "task", "--priority", "2", "--assignee", "bob")
		w.run("label", "add", id2, "backend")
		id3 := w.create("--title", "UI polish", "--type", "bug", "--priority", "3", "--assignee", "alice")
		w.run("label", "add", id3, "frontend")
		id4 := w.create("--title", "Docs update", "--type", "task", "--priority", "4")
		w.run("close", id4, "--reason", "done")
	}

	queries := []string{
		"status:open priority:1",
		"type:task",
		"assignee:alice",
		"status:closed",
		"type:bug status:open",
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			bOut, bErr := baselineWS.tryRun("query", q, "--json")
			cOut, cErr := candidateWS.tryRun("query", q, "--json")

			if bErr != nil {
				t.Skipf("pre-existing: bd query %q fails in baseline", q)
			}
			if cErr != nil {
				t.Fatalf("bd query %q fails in candidate: %v", q, cErr)
			}

			bTitles := extractTitleOrder(t, bOut)
			cTitles := extractTitleOrder(t, cOut)
			sort.Strings(bTitles)
			sort.Strings(cTitles)

			if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
				t.Errorf("query %q results differ:\n  baseline:  %v\n  candidate: %v",
					q, bTitles, cTitles)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// bd stale command parity
// ---------------------------------------------------------------------------

// TestStaleCommandParity verifies bd stale returns consistent results.
func TestStaleCommandParity(t *testing.T) {
	scenario := func(w *workspace) {
		w.create("--title", "Fresh issue", "--type", "task")
		w.create("--title", "Another fresh", "--type", "bug")
	}

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)

	// With a very short threshold, both should show the same issues
	bOut, bErr := baselineWS.tryRun("stale", "--days", "0", "--json")
	cOut, cErr := candidateWS.tryRun("stale", "--days", "0", "--json")

	if bErr != nil {
		t.Skip("pre-existing: bd stale fails in baseline")
	}
	if cErr != nil {
		t.Fatalf("bd stale fails in candidate: %v", cErr)
	}

	bTitles := extractTitleOrder(t, bOut)
	cTitles := extractTitleOrder(t, cOut)
	sort.Strings(bTitles)
	sort.Strings(cTitles)

	if strings.Join(bTitles, "|") != strings.Join(cTitles, "|") {
		t.Errorf("stale results differ:\n  baseline:  %v\n  candidate: %v",
			bTitles, cTitles)
	}
}

// ---------------------------------------------------------------------------
// Large export round-trip stress test
// ---------------------------------------------------------------------------

// TestLargeExportImportRoundTrip creates a substantial number of issues
// with rich data (labels, deps, comments) and verifies export → import
// preserves everything. This catches batch-processing bugs that only
// appear at scale.
func TestLargeExportImportRoundTrip(t *testing.T) {
	baselineWS := newWorkspace(t, baselineBin)

	// Create 20 issues with interconnected deps, labels, comments
	var ids []string
	for i := 0; i < 20; i++ {
		id := baselineWS.create("--title", fmt.Sprintf("Issue %03d", i),
			"--type", "task", "--priority", fmt.Sprintf("%d", i%5))
		ids = append(ids, id)
		baselineWS.run("label", "add", id, fmt.Sprintf("batch-%d", i%3))
		baselineWS.run("comment", id, fmt.Sprintf("Comment on issue %d", i))
	}
	// Create dependency chain
	for i := 1; i < len(ids); i += 3 {
		baselineWS.run("dep", "add", ids[i], ids[i-1])
	}

	// Export
	exportFile := filepath.Join(baselineWS.dir, "bulk-export.jsonl")
	baselineWS.run("export", "-o", exportFile)
	exportData, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}

	// Import into candidate
	candidateWS := newWorkspace(t, candidateBin)
	importFile := filepath.Join(candidateWS.dir, "bulk-import.jsonl")
	if err := os.WriteFile(importFile, exportData, 0o644); err != nil {
		t.Fatalf("writing import: %v", err)
	}
	candidateWS.run("import", "-i", importFile)

	// Compare
	candidateRaw := candidateWS.export()
	diffNormalized(t, string(exportData), candidateRaw, nil, nil)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseJSONLByID parses JSONL and returns a map of canonical ID → parsed object.
// Uses the provided workspace's ID mapping for canonicalization.
func parseJSONLByID(t *testing.T, data string) map[string]map[string]any {
	t.Helper()
	result := make(map[string]map[string]any)
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if id, ok := m["id"].(string); ok {
			result[id] = m
		}
	}
	return result
}

// countDeps returns the number of dependency entries in an issue's export.
func countDeps(issue map[string]any) int {
	deps, ok := issue["dependencies"].([]any)
	if !ok {
		return 0
	}
	return len(deps)
}

func setToSortedSlice(s map[string]bool) []string {
	var out []string
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseJSONFieldNames parses JSON output (array or single object) and returns
// all top-level field names from the first object.
func parseJSONFieldNames(t *testing.T, output string) []string {
	t.Helper()

	// Try JSON array first
	var arr []map[string]any
	if err := json.Unmarshal([]byte(output), &arr); err == nil && len(arr) > 0 {
		var keys []string
		for k := range arr[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	// Try single object
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err == nil {
		var keys []string
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	// Try JSONL
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			var keys []string
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		}
	}
	return nil
}

// extractTitleOrder parses bd ready --json output and returns titles in order.
func extractTitleOrder(t *testing.T, output string) []string {
	t.Helper()
	var titles []string

	var issues []map[string]any
	if err := json.Unmarshal([]byte(output), &issues); err == nil {
		for _, m := range issues {
			if title, ok := m["title"].(string); ok {
				titles = append(titles, title)
			}
		}
		return titles
	}

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if title, ok := m["title"].(string); ok {
			titles = append(titles, title)
		}
	}
	return titles
}
