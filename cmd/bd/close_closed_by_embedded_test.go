//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedShowJSONReportsClosedBy reproduces GH#8617: bd records the actor
// that closed an issue (bd history --events prints "closed by <actor>"), but the
// issue JSON from `bd show --json` carries only closed_at and close_reason, so a
// consumer of the issue record cannot tell who closed it.
func TestEmbeddedShowJSONReportsClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	issue := bdCreate(t, bd, dir, "Closed by actor", "--type", "task", "--actor", "alice")
	bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--reason", "done")

	cmd := exec.Command(bd, "show", issue.ID, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in bd show output:\n%s", s)
	}
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	if s[start] == '[' {
		var arr []map[string]any
		if err := dec.Decode(&arr); err != nil || len(arr) == 0 {
			t.Fatalf("decode bd show --json array: %v\n%s", err, s[start:])
		}
		obj = arr[0]
	} else if err := dec.Decode(&obj); err != nil {
		t.Fatalf("decode bd show --json: %v\n%s", err, s[start:])
	}

	if obj["status"] != "closed" || obj["closed_at"] == nil {
		t.Fatalf("issue not closed in show JSON: status=%v closed_at=%v", obj["status"], obj["closed_at"])
	}
	if got, ok := obj["closed_by"]; !ok || got != "alice" {
		t.Errorf("bd show --json closed_by = %v (present=%v), want %q (the --actor that closed it)\nJSON: %s",
			got, ok, "alice", s[start:])
	}

	// The text view reports the closer too (GH#8617's "who closed this" half
	// for human readers), beside the close metadata it already carries.
	showOut, err := bdRunWithFlockRetry(t, bd, dir, "show", issue.ID)
	if err != nil {
		t.Fatalf("bd show %s failed: %v\n%s", issue.ID, err, showOut)
	}
	if !strings.Contains(string(showOut), "Closed by: alice") {
		t.Errorf("bd show text output missing %q:\n%s", "Closed by: alice", showOut)
	}

	// bd list --json reads the lite projection, so closed_by must ride
	// IssueSelectColumnsLite as well or the field vanishes on listing paths.
	listOut, err := bdRunWithFlockRetry(t, bd, dir, "list", "--status", "closed", "--json")
	if err != nil {
		t.Fatalf("bd list --status closed --json failed: %v\n%s", err, listOut)
	}
	ls := string(listOut)
	lstart := strings.IndexAny(ls, "[{")
	if lstart < 0 {
		t.Fatalf("no JSON in bd list output:\n%s", ls)
	}
	var listed []map[string]any
	if err := json.NewDecoder(strings.NewReader(ls[lstart:])).Decode(&listed); err != nil {
		var one map[string]any
		if e2 := json.NewDecoder(strings.NewReader(ls[lstart:])).Decode(&one); e2 != nil {
			t.Fatalf("decode bd list --json: %v\n%s", err, ls[lstart:])
		}
		listed = []map[string]any{one}
	}
	var found bool
	for _, row := range listed {
		if row["id"] != issue.ID {
			continue
		}
		found = true
		if got, ok := row["closed_by"]; !ok || got != "alice" {
			t.Errorf("bd list --json closed_by = %v (present=%v), want %q\nrow: %s",
				got, ok, "alice", ls[lstart:])
		}
	}
	if !found {
		t.Errorf("bd list --status closed --json does not list %s:\n%s", issue.ID, ls)
	}
}

// showJSONClosedBy runs `bd show <id> --json` and reports the decoded issue
// plus whether the closed_by member is present, so a test can assert both the
// value and the member's absence at a path that skips the stamp.
func showJSONClosedBy(t *testing.T, bd, dir, id string) (map[string]any, any, bool) {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in bd show output for %s:\n%s", id, s)
	}
	obj := map[string]any{}
	raw := s[start:]
	dec := json.NewDecoder(strings.NewReader(raw))
	if raw[0] == '[' {
		var arr []map[string]any
		if err := dec.Decode(&arr); err != nil || len(arr) == 0 {
			t.Fatalf("decode bd show --json array for %s: %v\n%s", id, err, raw)
		}
		obj = arr[0]
	} else if err := dec.Decode(&obj); err != nil {
		t.Fatalf("decode bd show --json for %s: %v\n%s", id, err, raw)
	}
	got, present := obj["closed_by"]
	return obj, got, present
}

// TestEmbeddedClosePathsStampClosedBy pins GH#8617 on every path that closes
// an issue, not just `bd close`: a generic status update that crosses into
// closed, a multi-id close, an issue created already closed, and a wisp close
// (the wisps plane) must all record the writing actor in closed_by. A
// batch-apply close routes through the same CloseIssueChecked/closeIssueInTx
// stamp `bd close` uses, so it is covered by the same write path.
func TestEmbeddedClosePathsStampClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	// A generic `bd update --status closed` (no close_reason, no session) is
	// the close path that does NOT go through bd close's checked-close
	// writer; ManageClosedAt must stamp the update's actor anyway.
	updated := bdCreate(t, bd, dir, "Close via update", "--type", "task")
	bdUpdate(t, bd, dir, updated.ID, "--actor", "bob", "--status", "closed")
	obj, got, present := showJSONClosedBy(t, bd, dir, updated.ID)
	if obj["status"] != "closed" {
		t.Fatalf("update close: status = %v, want closed\nJSON: %s", obj["status"], obj)
	}
	if !present || got != "bob" {
		t.Errorf("update close: closed_by = %v (present=%v), want %q (the update's --actor)\nJSON: %s",
			got, present, "bob", obj)
	}

	// A multi-id close stamps every row it closes with the batch's actor.
	m1 := bdCreate(t, bd, dir, "Batch close one", "--type", "task")
	m2 := bdCreate(t, bd, dir, "Batch close two", "--type", "task")
	bdClose(t, bd, dir, m1.ID, m2.ID, "--actor", "carol", "--reason", "batch")
	for _, id := range []string{m1.ID, m2.ID} {
		_, got, present := showJSONClosedBy(t, bd, dir, id)
		if !present || got != "carol" {
			t.Errorf("multi-id close %s: closed_by = %v (present=%v), want %q", id, got, present, "carol")
		}
	}

	// An issue created already closed has no separate closing write, so the
	// creator (the create request's actor) is who closed it.
	bornClosed := bdCreate(t, bd, dir, "Born closed", "--type", "task", "--actor", "dave", "-s", "closed")
	obj, got, present = showJSONClosedBy(t, bd, dir, bornClosed.ID)
	if obj["status"] != "closed" {
		t.Fatalf("create -s closed: status = %v, want closed\nJSON: %s", obj["status"], obj)
	}
	if !present || got != "dave" {
		t.Errorf("create -s closed: closed_by = %v (present=%v), want %q (the create's --actor)\nJSON: %s",
			got, present, "dave", obj)
	}

	// A wisp (ephemeral) close lives on the wisps plane; the stamp must ride
	// the same close writer there.
	wisp := bdCreate(t, bd, dir, "Wisp close", "--ephemeral", "--actor", "erin")
	bdClose(t, bd, dir, wisp.ID, "--actor", "erin", "--reason", "done")
	obj, got, present = showJSONClosedBy(t, bd, dir, wisp.ID)
	if obj["status"] != "closed" {
		t.Fatalf("wisp close: status = %v, want closed\nJSON: %s", obj["status"], obj)
	}
	if !present || got != "erin" {
		t.Errorf("wisp close: closed_by = %v (present=%v), want %q\nJSON: %s", got, present, "erin", obj)
	}
}

// TestEmbeddedReopenClearsClosedByAndReCloseRestamps pins the reopen half of
// GH#8617: reopening forgets who closed the issue (the row is no longer
// closed, so the closer of the previous close must not linger), and the next
// close records ITS actor — not the earlier closer.
func TestEmbeddedReopenClearsClosedByAndReCloseRestamps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	issue := bdCreate(t, bd, dir, "Reopen clears closer", "--type", "task")
	bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--reason", "first")

	bdReopen(t, bd, dir, issue.ID, "--reason", "not done")

	obj, got, present := showJSONClosedBy(t, bd, dir, issue.ID)
	if obj["status"] != "open" {
		t.Fatalf("after reopen: status = %v, want open\nJSON: %s", obj["status"], obj)
	}
	if present && got != "" {
		t.Errorf("after reopen: closed_by = %v (present=%v), want absent/empty; a reopened issue has no closer\nJSON: %s",
			got, present, obj)
	}

	bdClose(t, bd, dir, issue.ID, "--actor", "bob", "--reason", "second")
	obj, got, present = showJSONClosedBy(t, bd, dir, issue.ID)
	if obj["status"] != "closed" {
		t.Fatalf("after re-close: status = %v, want closed\nJSON: %s", obj["status"], obj)
	}
	if !present || got != "bob" {
		t.Errorf("after re-close: closed_by = %v (present=%v), want %q (the SECOND closer, not alice)\nJSON: %s",
			got, present, "bob", obj)
	}
}

// TestEmbeddedExportImportPreservesClosedBy pins the snapshot half of
// GH#8617: `bd export` carries closed_by, an import into a fresh workspace
// restores the ORIGINAL closer, and a legacy closed row with no closed_by
// member imports empty — the importing user is never stamped as the closer
// (the import path writes the snapshot's fields through PrepareIssueForInsert,
// never through the create default that would stamp the request's actor).
func TestEmbeddedExportImportPreservesClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	srcDir, _, _ := bdInit(t, bd, "--prefix", "cb")

	closed := bdCreate(t, bd, srcDir, "Round trip closer", "--type", "task")
	bdClose(t, bd, srcDir, closed.ID, "--actor", "alice", "--reason", "rt")

	// The export carries the closer on the closed row.
	export := bdExport(t, bd, srcDir)
	var exportedLine string
	for _, line := range strings.Split(strings.TrimSpace(export), "\n") {
		if strings.Contains(line, `"id":"`+closed.ID+`"`) {
			exportedLine = line
		}
	}
	if exportedLine == "" {
		t.Fatalf("bd export does not carry %s:\n%s", closed.ID, export)
	}
	if !strings.Contains(exportedLine, `"closed_by":"alice"`) {
		t.Errorf("export line for %s missing closed_by=alice:\n%s", closed.ID, exportedLine)
	}

	// Import into a fresh workspace: the ORIGINAL closer survives.
	exportPath := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(exportPath, []byte(export), 0644); err != nil {
		t.Fatalf("write export file: %v", err)
	}
	dstDir, _, _ := bdInit(t, bd, "--prefix", "cb")
	bdImport(t, bd, dstDir, exportPath)

	obj, got, present := showJSONClosedBy(t, bd, dstDir, closed.ID)
	if obj["status"] != "closed" {
		t.Fatalf("imported %s: status = %v, want closed\nJSON: %s", closed.ID, obj["status"], obj)
	}
	if !present || got != "alice" {
		t.Errorf("imported %s: closed_by = %v (present=%v), want %q (the exported closer, not the importer)\nJSON: %s",
			closed.ID, got, present, "alice", obj)
	}

	// A legacy closed row (exported before the column existed) carries no
	// closed_by member; importing it must leave the closer empty rather than
	// stamping whoever ran bd import.
	now := time.Now().UTC().Truncate(time.Second)
	legacy := types.Issue{
		ID: "cb-legacy", Title: "Legacy closed row", Status: types.StatusClosed,
		IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now, ClosedAt: &now,
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	writeJSONLFile(t, legacyPath, []types.Issue{legacy})
	bdImport(t, bd, dstDir, legacyPath)

	obj, got, present = showJSONClosedBy(t, bd, dstDir, legacy.ID)
	if obj["status"] != "closed" {
		t.Fatalf("imported legacy %s: status = %v, want closed\nJSON: %s", legacy.ID, obj["status"], obj)
	}
	if present && got != "" {
		t.Errorf("imported legacy %s: closed_by = %v (present=%v), want absent/empty (import never stamps the importer)\nJSON: %s",
			legacy.ID, got, present, obj)
	}
}
