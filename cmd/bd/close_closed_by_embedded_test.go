//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
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
