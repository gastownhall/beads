//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
}

// closedByRunJSON runs bd with extra environment and decodes the JSON value
// stdout holds (an object or an array of objects) into objects.
func closedByRunJSON(t *testing.T, bd, dir string, extraEnv []string, args ...string) []map[string]any {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in bd %s output:\n%s", strings.Join(args, " "), s)
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	if s[start] == '[' {
		var arr []map[string]any
		if err := dec.Decode(&arr); err != nil {
			t.Fatalf("decode bd %s: %v\n%s", strings.Join(args, " "), err, s[start:])
		}
		return arr
	}
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("decode bd %s: %v\n%s", strings.Join(args, " "), err, s[start:])
	}
	return []map[string]any{obj}
}

// closedByShow returns the issue `bd show <id> --json` prints.
func closedByShow(t *testing.T, bd, dir, id string) map[string]any {
	t.Helper()
	objs := closedByRunJSON(t, bd, dir, nil, "show", id, "--json")
	if len(objs) == 0 {
		t.Fatalf("bd show %s --json printed no issue", id)
	}
	return objs[0]
}

// assertClosedBy requires the issue's JSON to carry closed_by == want, or to
// omit the key when want is empty.
func assertClosedBy(t *testing.T, label string, obj map[string]any, want string) {
	t.Helper()
	got, present := obj["closed_by"]
	if want == "" {
		if present {
			t.Errorf("%s: closed_by = %v, want the key absent", label, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s: closed_by = %v (present=%v), want %q\nissue: %v", label, got, present, want, obj)
	}
}

// TestEmbeddedCloseStampsClosedBy pins AC-1 of GH#8617: `bd close` records its
// actor, from --actor or from BEADS_ACTOR, and bd show (JSON and text) and
// bd list --json read it back.
func TestEmbeddedCloseStampsClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	byFlag := bdCreate(t, bd, dir, "Closed with --actor", "--type", "task", "--actor", "creator")
	byEnv := bdCreate(t, bd, dir, "Closed with BEADS_ACTOR", "--type", "task", "--actor", "creator")
	open := bdCreate(t, bd, dir, "Never closed", "--type", "task", "--actor", "creator")

	bdClose(t, bd, dir, byFlag.ID, "--actor", "closer-a", "--reason", "done")
	closedByRunJSON(t, bd, dir, []string{"BEADS_ACTOR=closer-env"}, "close", byEnv.ID, "--json")

	assertClosedBy(t, "show after close --actor", closedByShow(t, bd, dir, byFlag.ID), "closer-a")
	assertClosedBy(t, "show after BEADS_ACTOR close", closedByShow(t, bd, dir, byEnv.ID), "closer-env")
	assertClosedBy(t, "show of an open issue", closedByShow(t, bd, dir, open.ID), "")

	listed := map[string]map[string]any{}
	for _, obj := range closedByRunJSON(t, bd, dir, nil, "list", "--status", "closed", "--json") {
		if id, _ := obj["id"].(string); id != "" {
			listed[id] = obj
		}
	}
	for id, want := range map[string]string{byFlag.ID: "closer-a", byEnv.ID: "closer-env"} {
		obj, ok := listed[id]
		if !ok {
			t.Errorf("bd list --status closed --json has no row for %s", id)
			continue
		}
		assertClosedBy(t, "list --status closed --json row "+id, obj, want)
	}

	text := bdShowRaw(t, bd, dir, byFlag.ID)
	if !strings.Contains(text, "Closed by: closer-a") {
		t.Errorf("bd show %s text lacks %q:\n%s", byFlag.ID, "Closed by: closer-a", text)
	}
	if openText := bdShowRaw(t, bd, dir, open.ID); strings.Contains(openText, "Closed by:") {
		t.Errorf("bd show of an open issue prints a closer:\n%s", openText)
	}
}

// TestEmbeddedCloseRoutesStampClosedBy pins AC-2 of GH#8617: every other write
// that lands a closed row records that write's actor — update -s closed, a
// multi-id close (one stamp per id), a bd batch close, a create that starts
// closed, and a wisp close.
func TestEmbeddedCloseRoutesStampClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cb")

	viaUpdate := bdCreate(t, bd, dir, "Closed by update", "--type", "task", "--actor", "creator")
	bdUpdate(t, bd, dir, viaUpdate.ID, "-s", "closed", "--actor", "updater-b")
	assertClosedBy(t, "update -s closed", closedByShow(t, bd, dir, viaUpdate.ID), "updater-b")

	first := bdCreate(t, bd, dir, "Multi close one", "--type", "task", "--actor", "creator")
	second := bdCreate(t, bd, dir, "Multi close two", "--type", "task", "--actor", "creator")
	bdClose(t, bd, dir, first.ID, second.ID, "--actor", "multi-m")
	assertClosedBy(t, "multi-id close (first)", closedByShow(t, bd, dir, first.ID), "multi-m")
	assertClosedBy(t, "multi-id close (second)", closedByShow(t, bd, dir, second.ID), "multi-m")

	viaBatch := bdCreate(t, bd, dir, "Closed by batch", "--type", "task", "--actor", "creator")
	batch := exec.Command(bd, "batch", "--actor", "batcher")
	batch.Dir = dir
	batch.Env = bdEnv(dir)
	batch.Stdin = strings.NewReader("close " + viaBatch.ID + " batched\n")
	if out, err := batch.CombinedOutput(); err != nil {
		t.Fatalf("bd batch close failed: %v\n%s", err, out)
	}
	assertClosedBy(t, "bd batch close", closedByShow(t, bd, dir, viaBatch.ID), "batcher")

	created := bdCreate(t, bd, dir, "Created closed", "--type", "task", "-s", "closed", "--actor", "creator-c")
	assertClosedBy(t, "create -s closed", closedByShow(t, bd, dir, created.ID), "creator-c")

	wisp := bdCreate(t, bd, dir, "Wisp closed", "--type", "task", "--ephemeral", "--actor", "creator")
	bdClose(t, bd, dir, wisp.ID, "--actor", "wisp-w")
	assertClosedBy(t, "wisp close", closedByShow(t, bd, dir, wisp.ID), "wisp-w")
}

// TestEmbeddedExportImportKeepsClosedBy pins AC-4 of GH#8617: export carries
// closed_by, import into a fresh workspace keeps it, and an imported closed
// row with no closer stays empty — import never stamps the importer.
func TestEmbeddedExportImportKeepsClosedBy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	src, _, _ := bdInit(t, bd, "--prefix", "cb")

	closed := bdCreate(t, bd, src, "Exported closed", "--type", "task", "--actor", "creator")
	bdClose(t, bd, src, closed.ID, "--actor", "closer-a", "--reason", "done")

	exported := bdExport(t, bd, src)
	if !strings.Contains(exported, `"closed_by":"closer-a"`) {
		t.Fatalf("bd export does not carry closed_by:\n%s", exported)
	}

	// A closed row from a writer that never recorded a closer: the import
	// must leave its closed_by empty rather than stamp the importer.
	legacy := `{"id":"cb-legacy1","title":"Closed before closed_by","status":"closed","priority":2,` +
		`"issue_type":"task","created_at":"2025-01-02T03:04:05Z","updated_at":"2025-01-02T03:04:05Z",` +
		`"closed_at":"2025-01-02T03:04:05Z","close_reason":"old"}`
	jsonl := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(jsonl, []byte(strings.TrimRight(exported, "\n")+"\n"+legacy+"\n"), 0o644); err != nil {
		t.Fatalf("write JSONL: %v", err)
	}

	dst, _, _ := bdInit(t, bd, "--prefix", "cb")
	cmd := exec.Command(bd, "import", jsonl, "--actor", "importer")
	cmd.Dir = dst
	cmd.Env = bdEnv(dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd import failed: %v\n%s", err, out)
	}

	assertClosedBy(t, "imported closed issue", closedByShow(t, bd, dst, closed.ID), "closer-a")
	legacyRow := closedByShow(t, bd, dst, "cb-legacy1")
	if legacyRow["status"] != "closed" {
		t.Fatalf("imported legacy row status = %v, want closed", legacyRow["status"])
	}
	assertClosedBy(t, "imported closed row without a closer", legacyRow, "")
}
