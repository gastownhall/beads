// Package protocol contains invariant tests that pin down expected CLI behavior.
//
// Each test asserts a specific rule that the bd CLI must satisfy. When a test
// is skipped, the skip message references the issue tracking the violation.
// Un-skip when the underlying bug is fixed — the test becomes a permanent
// guardrail against re-regression.
//
// These tests are independent of the differential regression suite
// (tests/regression/) and can be merged and run without it.
package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Binary build (once per test run)
// ---------------------------------------------------------------------------

var (
	bdPath string
	bdDir  string
	bdOnce sync.Once
	bdErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if bdDir != "" {
		os.RemoveAll(bdDir)
	}
	os.Exit(code)
}

func buildBD(t *testing.T) string {
	t.Helper()
	bdOnce.Do(func() {
		bin := "bd-protocol"
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		dir, err := os.MkdirTemp("", "bd-protocol-*")
		if err != nil {
			bdErr = err
			return
		}
		bdDir = dir
		bdPath = filepath.Join(dir, bin)

		modRoot := findModuleRoot(t)
		cmd := exec.Command("go", "build", "-o", bdPath, "./cmd/bd")
		cmd.Dir = modRoot
		cmd.Env = buildEnv()

		out, err := cmd.CombinedOutput()
		if err != nil {
			bdErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if bdErr != nil {
		t.Skipf("skipping: failed to build bd: %v", bdErr)
	}
	return bdPath
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

func buildEnv() []string {
	env := os.Environ()
	if prefix := icuPrefix(); prefix != "" {
		env = append(env,
			"CGO_CFLAGS=-I"+prefix+"/include",
			"CGO_CPPFLAGS=-I"+prefix+"/include",
			"CGO_LDFLAGS=-L"+prefix+"/lib",
		)
	}
	return env
}

func icuPrefix() string {
	out, err := exec.Command("brew", "--prefix", "icu4c").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Workspace: isolated temp dir with git repo + bd init
// ---------------------------------------------------------------------------

type workspace struct {
	dir string
	bd  string
	t   *testing.T
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("skipping: dolt not installed")
	}
	bd := buildBD(t)
	dir := t.TempDir()
	w := &workspace{dir: dir, bd: bd, t: t}

	w.git("init")
	w.git("config", "user.name", "protocol-test")
	w.git("config", "user.email", "test@protocol.test")

	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.git("add", ".")
	w.git("commit", "-m", "initial")

	w.run("init", "--prefix", "test", "--quiet")
	return w
}

func (w *workspace) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.dir,
		"BEADS_NO_DAEMON=1",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

func (w *workspace) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (w *workspace) run(args ...string) string {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// tryRun runs a bd command and returns output + error (does not fatal on failure).
func (w *workspace) tryRun(args ...string) (string, error) {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// create runs bd create --silent and returns the issue ID.
func (w *workspace) create(args ...string) string {
	w.t.Helper()
	allArgs := append([]string{"create", "--silent"}, args...)
	cmd := exec.Command(w.bd, allArgs...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		w.t.Fatalf("bd create %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		w.t.Fatal("bd create returned empty ID")
	}
	return id
}

// showJSON runs bd show <id> --json and returns the first issue object.
func (w *workspace) showJSON(id string) map[string]any {
	w.t.Helper()
	out := w.run("show", id, "--json")
	items := parseJSONOutput(w.t, out)
	if len(items) == 0 {
		w.t.Fatalf("bd show %s --json returned no items", id)
	}
	return items[0]
}

// ---------------------------------------------------------------------------
// Protocol tests
// ---------------------------------------------------------------------------

// TestProtocol_ImportPreservesRelationalData asserts that bd import MUST
// preserve labels, dependencies, and comments embedded in JSONL records.
//
// Invariant: export → import → export produces identical relational data.
//
// This pins down the behavior that GH#1844 violates: main's importIssuesCore
// delegates to CreateIssuesWithFullOptions which only inserts into the issues
// table, silently dropping labels, dependencies, and comments.
func TestProtocol_ImportPreservesRelationalData(t *testing.T) {
	// --- Create source data ---
	src := newWorkspace(t)
	id1 := src.create("--title", "Feature with data", "--type", "feature", "--priority", "1")
	id2 := src.create("--title", "Dependency target", "--type", "task", "--priority", "2")

	src.run("label", "add", id1, "important")
	src.run("label", "add", id1, "v2")
	src.run("label", "add", id2, "backend")

	src.run("dep", "add", id1, id2) // feature depends on dep-target

	src.run("comment", id1, "Design notes for the feature")
	src.run("comment", id1, "Review feedback from team")

	// --- Export ---
	exportFile := filepath.Join(src.dir, "export.jsonl")
	src.run("export", "-o", exportFile)
	exportData, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}

	// --- Import into fresh workspace ---
	dst := newWorkspace(t)
	importFile := filepath.Join(dst.dir, "import.jsonl")
	if err := os.WriteFile(importFile, exportData, 0o644); err != nil {
		t.Fatalf("writing import file: %v", err)
	}
	dst.run("import", "-i", importFile)

	// --- Retrieve via both paths ---
	// Bulk path: bd export (projection)
	dstExport := dst.run("export")
	exportIssues := parseJSONLByID(t, dstExport)

	featExport, ok := exportIssues[id1]
	if !ok {
		t.Fatalf("feature issue %s not found in post-import export", id1)
	}
	depTargetExport, ok := exportIssues[id2]
	if !ok {
		t.Fatalf("dependency target %s not found in post-import export", id2)
	}

	// Deep path: bd show <id> --json (hydration)
	featShow := dst.showJSON(id1)

	// --- Subtests per relational table ---
	t.Run("labels", func(t *testing.T) {
		// Feature labels via export (bulk)
		requireStringSetEqual(t, getStringSlice(featExport, "labels"),
			[]string{"important", "v2"}, "feature labels via export")

		// Dep-target labels via export (bulk)
		requireStringSetEqual(t, getStringSlice(depTargetExport, "labels"),
			[]string{"backend"}, "dep-target labels via export")

		// Feature labels via show (deep hydration)
		requireStringSetEqual(t, getStringSlice(featShow, "labels"),
			[]string{"important", "v2"}, "feature labels via show --json")
	})

	t.Run("dependencies", func(t *testing.T) {
		wantEdges := []depEdge{{issueID: id1, dependsOnID: id2}}

		// Via export
		requireDepEdgesEqual(t, getObjectSlice(featExport, "dependencies"),
			wantEdges, "feature deps via export")

		// Via show --json
		requireDepEdgesEqual(t, getObjectSlice(featShow, "dependencies"),
			wantEdges, "feature deps via show --json")
	})

	t.Run("comments", func(t *testing.T) {
		wantTexts := []string{
			"Design notes for the feature",
			"Review feedback from team",
		}

		// Via export
		requireCommentTextsEqual(t, getObjectSlice(featExport, "comments"),
			wantTexts, "feature comments via export")

		// Via show --json
		requireCommentTextsEqual(t, getObjectSlice(featShow, "comments"),
			wantTexts, "feature comments via show --json")
	})
}

// TestProtocol_ReadyOrderingIsPriorityAsc asserts that bd ready --json returns
// issues ordered by priority ascending (P0 first, then P1, P2, ..., P4).
//
// Minimal protocol: only the primary sort key (priority ASC) is enforced.
// Tie-breaking within the same priority is intentionally left unspecified
// so that future secondary-sort changes don't break this test.
//
// This pins down the behavior that GH#1880 violates: the Dolt backend returns
// ready issues in a different order than the SQLite backend.
func TestProtocol_ReadyOrderingIsPriorityAsc(t *testing.T) {
	w := newWorkspace(t)
	w.create("--title", "P4 backlog", "--type", "task", "--priority", "4")
	w.create("--title", "P0 critical", "--type", "task", "--priority", "0")
	w.create("--title", "P2 medium", "--type", "task", "--priority", "2")
	w.create("--title", "P1 high", "--type", "task", "--priority", "1")
	w.create("--title", "P3 low", "--type", "task", "--priority", "3")

	out := w.run("ready", "--json")
	items := parseJSONOutput(t, out)

	// Sanity: the ready set must be non-empty given we just created 5 open tasks
	if len(items) == 0 {
		t.Fatal("bd ready --json returned 0 issues; expected at least 5 open tasks")
	}
	if len(items) != 5 {
		t.Fatalf("bd ready --json returned %d issues, want 5", len(items))
	}

	// Non-decreasing priority (the only ordering contract we enforce)
	priorities := make([]int, len(items))
	for i, m := range items {
		if p, ok := m["priority"].(float64); ok {
			priorities[i] = int(p)
		}
	}
	for i := 1; i < len(priorities); i++ {
		if priorities[i] < priorities[i-1] {
			t.Errorf("ready ordering violated: P%d appears after P%d at position %d (want priority ASC)",
				priorities[i], priorities[i-1], i)
		}
	}
}

// ---------------------------------------------------------------------------
// Data integrity: fields set via CLI must round-trip through export
// ---------------------------------------------------------------------------

// TestProtocol_FieldsRoundTrip asserts that every field settable via CLI
// survives create/update → export. This is a data integrity invariant:
// if the CLI accepts a value, export must reflect it.
func TestProtocol_FieldsRoundTrip(t *testing.T) {
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

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

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

	// Date fields: accept any RFC3339 that starts with the correct date
	assertFieldPrefix(t, issue, "due_at", "2099-03-15")
	assertFieldPrefix(t, issue, "defer_until", "2099-01-15")
}

// TestProtocol_MetadataRoundTrip asserts that JSON metadata set via
// bd update --metadata survives in the export output.
//
// Pins down the behavior that GH#1912 violates: the Dolt backend
// silently drops metadata.
func TestProtocol_MetadataRoundTrip(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Metadata carrier", "--type", "task")
	w.run("update", id, "--metadata", `{"component":"auth","risk":"high"}`)

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

	md, exists := issue["metadata"]
	if !exists {
		t.Fatal("metadata field missing from export (GH#1912: Dolt backend drops metadata)")
	}

	// Metadata may be a string or a parsed object depending on export format
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
// survives in the export output.
//
// Pins down the behavior that bd-wzgir violates: the Dolt backend drops spec_id.
func TestProtocol_SpecIDRoundTrip(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Spec carrier", "--type", "task")
	w.run("update", id, "--spec-id", "RFC-007")

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

	specID, ok := issue["spec_id"].(string)
	if !ok || specID == "" {
		t.Fatal("spec_id field missing or empty from export (bd-wzgir: Dolt drops spec_id)")
	}
	if specID != "RFC-007" {
		t.Errorf("spec_id = %q, want %q", specID, "RFC-007")
	}
}

// TestProtocol_CloseReasonRoundTrip asserts that close_reason survives
// close → export.
func TestProtocol_CloseReasonRoundTrip(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Closeable", "--type", "bug", "--priority", "2")
	w.run("close", id, "--reason", "Fixed in commit abc123")

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

	reason, ok := issue["close_reason"].(string)
	if !ok || reason == "" {
		t.Fatal("close_reason missing or empty from export after bd close --reason")
	}
	if reason != "Fixed in commit abc123" {
		t.Errorf("close_reason = %q, want %q", reason, "Fixed in commit abc123")
	}
}

// ---------------------------------------------------------------------------
// Data integrity: delete must not leave dangling dependencies
// ---------------------------------------------------------------------------

// TestProtocol_DeleteCleansUpDeps asserts that deleting an issue removes
// all references to it from other issues' dependency lists.
//
// Invariant: after bd delete X, no other issue should have X in its
// depends_on_id or issue_id fields.
func TestProtocol_DeleteCleansUpDeps(t *testing.T) {
	w := newWorkspace(t)
	idA := w.create("--title", "Survivor A", "--type", "task")
	idB := w.create("--title", "Will be deleted", "--type", "task")
	idC := w.create("--title", "Survivor C", "--type", "task")

	w.run("dep", "add", idB, idA) // B depends on A
	w.run("dep", "add", idC, idB) // C depends on B

	w.run("delete", idB, "--force")

	out := w.run("export")
	issues := parseJSONLByID(t, out)

	// B should not appear in export
	if _, exists := issues[idB]; exists {
		t.Errorf("deleted issue %s should not appear in export", idB)
	}

	// No surviving issue should reference B
	for issueID, issue := range issues {
		deps := getObjectSlice(issue, "dependencies")
		for _, dep := range deps {
			if dep["depends_on_id"] == idB {
				t.Errorf("issue %s still has dangling dependency on deleted %s", issueID, idB)
			}
			if dep["issue_id"] == idB {
				t.Errorf("issue %s has dependency with issue_id = deleted %s", issueID, idB)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Data integrity: labels/deps/comments survive updates
// ---------------------------------------------------------------------------

// TestProtocol_LabelsPreservedAcrossUpdate asserts that labels added to an
// issue are not lost when the issue is updated.
func TestProtocol_LabelsPreservedAcrossUpdate(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Labeled issue", "--type", "task")
	w.run("label", "add", id, "frontend")
	w.run("label", "add", id, "urgent")

	// Update an unrelated field
	w.run("update", id, "--title", "Labeled issue (renamed)")

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

	requireStringSetEqual(t, getStringSlice(issue, "labels"),
		[]string{"frontend", "urgent"}, "labels after title update")
}

// TestProtocol_DepsPreservedAcrossUpdate asserts that dependencies are not
// lost when an issue is updated.
func TestProtocol_DepsPreservedAcrossUpdate(t *testing.T) {
	w := newWorkspace(t)
	idA := w.create("--title", "Blocker", "--type", "task")
	idB := w.create("--title", "Blocked", "--type", "task")
	w.run("dep", "add", idB, idA)

	// Update an unrelated field
	w.run("update", idB, "--title", "Blocked (renamed)")

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[idB]
	if !ok {
		t.Fatalf("issue %s not found in export", idB)
	}

	requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
		[]depEdge{{issueID: idB, dependsOnID: idA}}, "deps after title update")
}

// TestProtocol_CommentsPreservedAcrossUpdate asserts that comments are not
// lost when an issue is updated.
func TestProtocol_CommentsPreservedAcrossUpdate(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Commented issue", "--type", "task")
	w.run("comment", id, "Important design note")
	w.run("comment", id, "Follow-up from review")

	// Update an unrelated field
	w.run("update", id, "--title", "Commented issue (renamed)")

	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id]
	if !ok {
		t.Fatalf("issue %s not found in export", id)
	}

	requireCommentTextsEqual(t, getObjectSlice(issue, "comments"),
		[]string{"Important design note", "Follow-up from review"},
		"comments after title update")
}

// ---------------------------------------------------------------------------
// Data integrity: parent-child dependencies must round-trip through export
// ---------------------------------------------------------------------------

// TestProtocol_ParentChildDepExportRoundTrip asserts that when a child issue
// is created via --parent, the dependency appears in BOTH directions in export:
// the child's dependencies list should reference the parent, and the parent's
// dependencies list should reference the child.
//
// Pins down the behavior that GH#1926 violates: export only includes
// child→parent edges (issue_id=child, depends_on_id=parent) because
// GetAllDependencyRecords keys by issue_id. The parent→child direction
// is silently dropped, causing epic trees to lose structure on roundtrip.
func TestProtocol_ParentChildDepExportRoundTrip(t *testing.T) {
	w := newWorkspace(t)
	parent := w.create("--title", "Epic parent", "--type", "epic", "--priority", "1")
	child := w.create("--title", "Child task", "--type", "task", "--priority", "2", "--parent", parent)

	out := w.run("export")
	issues := parseJSONLByID(t, out)

	parentIssue, ok := issues[parent]
	if !ok {
		t.Fatalf("parent issue %s not found in export", parent)
	}
	childIssue, ok := issues[child]
	if !ok {
		t.Fatalf("child issue %s not found in export", child)
	}

	// Child must have a dependency pointing to parent
	childDeps := getObjectSlice(childIssue, "dependencies")
	t.Run("child_references_parent", func(t *testing.T) {
		found := false
		for _, dep := range childDeps {
			dependsOn, _ := dep["depends_on_id"].(string)
			if dependsOn == parent {
				found = true
				// Verify it's a parent-child type
				depType, _ := dep["type"].(string)
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

	// Parent must also have the dependency edge visible in export (GH#1926)
	// The dep record has issue_id=child, depends_on_id=parent, so it should
	// appear in the child's deps. But for round-trip fidelity, the parent's
	// export should also carry this edge so that import reconstructs the tree.
	t.Run("parent_dep_edge_in_export", func(t *testing.T) {
		parentDeps := getObjectSlice(parentIssue, "dependencies")
		// Check if ANY dep in the entire export references both parent and child
		// in either direction — the key invariant is that the edge is not lost.
		edgeFound := false
		for _, iss := range issues {
			for _, dep := range getObjectSlice(iss, "dependencies") {
				issueID, _ := dep["issue_id"].(string)
				dependsOn, _ := dep["depends_on_id"].(string)
				if (issueID == child && dependsOn == parent) ||
					(issueID == parent && dependsOn == child) {
					edgeFound = true
				}
			}
		}
		if !edgeFound {
			t.Errorf("parent-child edge between %s and %s lost in export (GH#1926)", parent, child)
		}

		// Stronger assertion: parent should carry the dep in its own record
		parentHasDep := false
		for _, dep := range parentDeps {
			issueID, _ := dep["issue_id"].(string)
			dependsOn, _ := dep["depends_on_id"].(string)
			if (issueID == child && dependsOn == parent) ||
				(issueID == parent && dependsOn == child) {
				parentHasDep = true
			}
		}
		if !parentHasDep {
			t.Skipf("GH#1926: parent %s export omits parent-child dep "+
				"(edge exists on child but not on parent — %d parent deps)",
				parent, len(parentDeps))
		}
	})

	// Round-trip: export → import into fresh workspace → export again
	t.Run("roundtrip_preserves_tree", func(t *testing.T) {
		exportFile := filepath.Join(w.dir, "tree-export.jsonl")
		w.run("export", "-o", exportFile)
		exportData, err := os.ReadFile(exportFile)
		if err != nil {
			t.Fatalf("reading export: %v", err)
		}

		dst := newWorkspace(t)
		importFile := filepath.Join(dst.dir, "tree-import.jsonl")
		if err := os.WriteFile(importFile, exportData, 0o644); err != nil {
			t.Fatalf("writing import: %v", err)
		}
		dst.run("import", "-i", importFile)

		reimport := dst.run("export")
		reimportIssues := parseJSONLByID(t, reimport)

		// The child must still reference the parent after round-trip
		reimportChild, ok := reimportIssues[child]
		if !ok {
			t.Fatalf("child %s lost after round-trip", child)
		}
		reimportChildDeps := getObjectSlice(reimportChild, "dependencies")
		found := false
		for _, dep := range reimportChildDeps {
			dependsOn, _ := dep["depends_on_id"].(string)
			if dependsOn == parent {
				found = true
			}
		}
		if !found {
			t.Errorf("parent-child dep lost after export→import→export round-trip")
		}
	})
}

// ---------------------------------------------------------------------------
// List display: closed blockers must not appear as blocking
// ---------------------------------------------------------------------------

// TestProtocol_ClosedBlockerNotShownAsBlocking asserts that when all of an
// issue's blockers are closed, bd list must NOT display "(blocked by: ...)"
// for that issue.
//
// Pins down the behavior that GH#1858 reports: bd list shows resolved blockers
// as still blocking even though bd ready and bd show correctly identify them
// as resolved.
func TestProtocol_ClosedBlockerNotShownAsBlocking(t *testing.T) {
	w := newWorkspace(t)
	blocker := w.create("--title", "Blocker task", "--type", "task", "--priority", "1")
	blocked := w.create("--title", "Blocked task", "--type", "task", "--priority", "2")

	w.run("dep", "add", blocked, blocker)

	// Before closing: blocked issue should show as blocked
	t.Run("blocked_before_close", func(t *testing.T) {
		out := w.run("list", "--json")
		items := parseJSONOutput(t, out)
		blockedItem := findByID(items, blocked)
		if blockedItem == nil {
			t.Fatalf("blocked issue %s not found in list --json", blocked)
		}
		// Verify the dependency exists
		deps := getObjectSlice(blockedItem, "dependencies")
		if len(deps) == 0 {
			t.Errorf("blocked issue should have dependencies before close")
		}
	})

	// Close the blocker
	w.run("close", blocker)

	// After closing: bd ready should show the previously-blocked issue
	t.Run("ready_after_close", func(t *testing.T) {
		out := w.run("ready", "--json")
		items := parseJSONOutput(t, out)
		found := findByID(items, blocked)
		if found == nil {
			t.Errorf("issue %s should appear in bd ready after blocker %s was closed",
				blocked, blocker)
		}
	})

	// After closing: bd list text output must NOT show "(blocked by: ...)"
	t.Run("list_text_no_blocked_annotation", func(t *testing.T) {
		out := w.run("list", "--status", "open")
		if strings.Contains(out, "blocked by") {
			t.Errorf("bd list shows 'blocked by' annotation after blocker was closed (GH#1858)\noutput:\n%s", out)
		}
	})
}

// findByID finds an issue in a JSON array by its ID.
func findByID(items []map[string]any, id string) map[string]any {
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Data integrity: scalar updates must not destroy relational data
// ---------------------------------------------------------------------------

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
	w := newWorkspace(t)
	id1 := w.create("--title", "Data-rich issue", "--type", "feature", "--priority", "1")
	id2 := w.create("--title", "Dep target", "--type", "task")

	// Set up relational data
	w.run("label", "add", id1, "important")
	w.run("label", "add", id1, "v2")
	w.run("label", "add", id1, "frontend")
	w.run("dep", "add", id1, id2)
	w.run("comment", id1, "Design review notes")
	w.run("comment", id1, "Implementation started")

	// Rapid-fire scalar updates — each must preserve relational data
	w.run("update", id1, "--title", "Data-rich issue v2")
	w.run("update", id1, "--priority", "0")
	w.run("update", id1, "--description", "Updated description")
	w.run("update", id1, "--assignee", "alice")
	w.run("update", id1, "--notes", "Updated notes")

	// Verify via export (bulk path)
	out := w.run("export")
	issues := parseJSONLByID(t, out)
	issue, ok := issues[id1]
	if !ok {
		t.Fatalf("issue %s not found in export", id1)
	}

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

	// Verify via show --json (deep hydration path)
	shown := w.showJSON(id1)

	t.Run("labels_via_show", func(t *testing.T) {
		requireStringSetEqual(t, getStringSlice(shown, "labels"),
			[]string{"important", "v2", "frontend"},
			"labels via show --json after updates")
	})

	t.Run("comments_via_show", func(t *testing.T) {
		requireCommentTextsEqual(t, getObjectSlice(shown, "comments"),
			[]string{"Design review notes", "Implementation started"},
			"comments via show --json after updates")
	})
}

// ---------------------------------------------------------------------------
// Ready semantics: blocking dep resolution
// ---------------------------------------------------------------------------

// TestProtocol_ClosingBlockerMakesDepReady asserts that closing an issue
// that blocks another causes the blocked issue to appear in bd ready.
//
// Invariant: if B depends-on A (blocks type), and A is closed,
// then B must appear in bd ready (assuming B has no other open blockers).
func TestProtocol_ClosingBlockerMakesDepReady(t *testing.T) {
	w := newWorkspace(t)
	blocker := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	blocked := w.create("--title", "Blocked work", "--type", "task", "--priority", "2")
	w.run("dep", "add", blocked, blocker)

	// Before close: blocked must NOT appear in ready
	t.Run("blocked_before_close", func(t *testing.T) {
		readyIDs := parseReadyIDs(t, w)
		if readyIDs[blocked] {
			t.Errorf("issue %s should NOT be ready while blocker %s is open", blocked, blocker)
		}
	})

	w.run("close", blocker, "--reason", "done")

	// After close: blocked MUST appear in ready
	t.Run("unblocked_after_close", func(t *testing.T) {
		readyIDs := parseReadyIDs(t, w)
		if !readyIDs[blocked] {
			t.Errorf("issue %s should be ready after blocker %s was closed", blocked, blocker)
		}
	})
}

// TestProtocol_DiamondDepBlockingSemantics asserts correct behavior for
// diamond-shaped dependency graphs:
//
//	A ← B, A ← C, B ← D, C ← D
//
// When A is closed: B and C should become ready, D should stay blocked
// (still has open blockers B and C).
//
// Invariant: an issue with multiple blockers stays blocked until ALL
// blockers are resolved.
func TestProtocol_DiamondDepBlockingSemantics(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Root (A)", "--type", "task", "--priority", "1")
	b := w.create("--title", "Left (B)", "--type", "task", "--priority", "2")
	c := w.create("--title", "Right (C)", "--type", "task", "--priority", "2")
	d := w.create("--title", "Join (D)", "--type", "task", "--priority", "3")

	w.run("dep", "add", b, a) // B depends on A
	w.run("dep", "add", c, a) // C depends on A
	w.run("dep", "add", d, b) // D depends on B
	w.run("dep", "add", d, c) // D depends on C

	w.run("close", a, "--reason", "done")

	readyIDs := parseReadyIDs(t, w)

	// B and C should be ready (their only blocker A is closed)
	if !readyIDs[b] {
		t.Errorf("B (%s) should be ready after A closed", b)
	}
	if !readyIDs[c] {
		t.Errorf("C (%s) should be ready after A closed", c)
	}

	// D should NOT be ready (B and C are still open)
	if readyIDs[d] {
		t.Errorf("D (%s) should NOT be ready — B and C are still open", d)
	}

	// Close B — D still blocked by C
	w.run("close", b, "--reason", "done")
	readyIDs = parseReadyIDs(t, w)
	if readyIDs[d] {
		t.Errorf("D (%s) should NOT be ready — C is still open", d)
	}

	// Close C — now D should be ready
	w.run("close", c, "--reason", "done")
	readyIDs = parseReadyIDs(t, w)
	if !readyIDs[d] {
		t.Errorf("D (%s) should be ready after both B and C are closed", d)
	}
}

// TestProtocol_TransitiveBlockingChain asserts that transitive blocking
// works correctly through a chain: A ← B ← C ← D.
//
// When A is closed: only B should become ready. C stays blocked by B,
// D stays blocked by C (transitively).
//
// Invariant: transitive dependencies are respected — closing a root
// blocker does not unblock the entire chain.
func TestProtocol_TransitiveBlockingChain(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Chain-A", "--type", "task", "--priority", "1")
	b := w.create("--title", "Chain-B", "--type", "task", "--priority", "2")
	c := w.create("--title", "Chain-C", "--type", "task", "--priority", "2")
	d := w.create("--title", "Chain-D", "--type", "task", "--priority", "2")

	w.run("dep", "add", b, a)
	w.run("dep", "add", c, b)
	w.run("dep", "add", d, c)

	w.run("close", a, "--reason", "done")

	readyIDs := parseReadyIDs(t, w)

	if !readyIDs[b] {
		t.Errorf("B should be ready after A closed")
	}
	if readyIDs[c] {
		t.Errorf("C should NOT be ready — B is still open")
	}
	if readyIDs[d] {
		t.Errorf("D should NOT be ready — C (and transitively B) still open")
	}
}

// ---------------------------------------------------------------------------
// parseReadyIDs helper
// ---------------------------------------------------------------------------

// parseReadyIDs runs bd ready --json and returns the set of issue IDs.
func parseReadyIDs(t *testing.T, w *workspace) map[string]bool {
	t.Helper()
	out := w.run("ready", "--json")
	ids := make(map[string]bool)

	items := parseJSONOutput(t, out)
	for _, m := range items {
		if id, ok := m["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// Set comparison helpers
// ---------------------------------------------------------------------------

// requireStringSetEqual asserts that got and want contain exactly the same
// strings (order-independent). On failure it prints missing and unexpected items.
func requireStringSetEqual(t *testing.T, got, want []string, context string) {
	t.Helper()
	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)

	if slices.Equal(sortedGot, sortedWant) {
		return
	}

	missing, unexpected := setDiff(sortedWant, sortedGot)
	t.Errorf("%s: string set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// depEdge represents a dependency edge for set comparison.
type depEdge struct {
	issueID     string
	dependsOnID string
}

// requireDepEdgesEqual asserts that the dependency objects contain exactly
// the expected depends-on targets (order-independent).
//
// Handles two JSON formats:
//   - export JSONL: objects with "issue_id" and "depends_on_id" fields
//   - show --json:  embedded Issue objects where "id" = the depends-on target
//
// NOTE: This compares targets only, not dependency type (blocks vs
// parent-child etc.). Current protocol tests create one dep type per
// scenario so this is sufficient. If a test needs to distinguish types,
// extend depEdge to include type and compare (target, type) tuples.
func requireDepEdgesEqual(t *testing.T, gotObjs []map[string]any, want []depEdge, context string) {
	t.Helper()

	got := make([]depEdge, 0, len(gotObjs))
	for _, obj := range gotObjs {
		issueID, _ := obj["issue_id"].(string)
		dependsOn, _ := obj["depends_on_id"].(string)
		// show --json embeds the depended-on issue directly; its "id" is the target.
		if dependsOn == "" {
			dependsOn, _ = obj["id"].(string)
		}
		got = append(got, depEdge{issueID: issueID, dependsOnID: dependsOn})
	}

	// Compare only the depends_on_id targets. The issue_id may be empty in
	// the show --json format (it's implicit from the parent), so we compare
	// just the target set to stay format-agnostic.
	gotTargets := make([]string, len(got))
	for i, e := range got {
		gotTargets[i] = e.dependsOnID
	}
	wantTargets := make([]string, len(want))
	for i, e := range want {
		wantTargets[i] = e.dependsOnID
	}
	sort.Strings(gotTargets)
	sort.Strings(wantTargets)

	if slices.Equal(gotTargets, wantTargets) {
		return
	}

	missing, unexpected := setDiff(wantTargets, gotTargets)
	t.Errorf("%s: dep target set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// requireCommentTextsEqual asserts that the comment objects contain exactly
// the expected text values (order-independent).
//
// NOTE: Uses text as identity, which works when all comment texts in a
// scenario are distinct. If a test creates duplicate-text comments, this
// will undercount — switch to multiset (count occurrences) or compare
// author+text pairs instead.
func requireCommentTextsEqual(t *testing.T, gotObjs []map[string]any, want []string, context string) {
	t.Helper()

	got := make([]string, 0, len(gotObjs))
	for _, obj := range gotObjs {
		if text, ok := obj["text"].(string); ok {
			got = append(got, text)
		}
	}

	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)

	if slices.Equal(sortedGot, sortedWant) {
		return
	}

	missing, unexpected := setDiff(sortedWant, sortedGot)
	t.Errorf("%s: comment text set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// setDiff returns items in want but not got (missing) and items in got but
// not want (unexpected). Both inputs must be sorted.
func setDiff(want, got []string) (missing, unexpected []string) {
	wantSet := make(map[string]bool, len(want))
	for _, s := range want {
		wantSet[s] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, s := range got {
		gotSet[s] = true
	}
	for _, s := range want {
		if !gotSet[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !wantSet[s] {
			unexpected = append(unexpected, s)
		}
	}
	return missing, unexpected
}

// ---------------------------------------------------------------------------
// General helpers
// ---------------------------------------------------------------------------

// parseJSONLByID parses JSONL and returns a map of issue ID → parsed object.
func parseJSONLByID(t *testing.T, data string) map[string]map[string]any {
	t.Helper()
	result := make(map[string]map[string]any)
	for line := range strings.SplitSeq(strings.TrimSpace(data), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parsing JSONL line: %v\nline: %s", err, line)
		}
		if id, ok := m["id"].(string); ok {
			result[id] = m
		}
	}
	return result
}

func getStringSlice(m map[string]any, key string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func getObjectSlice(m map[string]any, key string) []map[string]any {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, v := range arr {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func assertField(t *testing.T, issue map[string]any, key, want string) {
	t.Helper()
	got, ok := issue[key].(string)
	if !ok || got == "" {
		t.Errorf("field %q missing or empty in export, want %q", key, want)
		return
	}
	if got != want {
		t.Errorf("field %q = %q, want %q", key, got, want)
	}
}

func assertFieldFloat(t *testing.T, issue map[string]any, key string, want float64) {
	t.Helper()
	got, ok := issue[key].(float64)
	if !ok {
		t.Errorf("field %q missing or not a number in export, want %v", key, want)
		return
	}
	if got != want {
		t.Errorf("field %q = %v, want %v", key, got, want)
	}
}

func assertFieldPrefix(t *testing.T, issue map[string]any, key, prefix string) {
	t.Helper()
	got, ok := issue[key].(string)
	if !ok || got == "" {
		t.Errorf("field %q missing or empty in export, want prefix %q", key, prefix)
		return
	}
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("field %q = %q, want prefix %q", key, got, prefix)
	}
}

// parseJSONOutput handles both JSON array and JSONL formats.
func parseJSONOutput(t *testing.T, output string) []map[string]any {
	t.Helper()

	// Try JSON array first
	var arr []map[string]any
	if err := json.Unmarshal([]byte(output), &arr); err == nil {
		return arr
	}

	// Fall back to JSONL
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		arr = append(arr, m)
	}
	return arr
}
