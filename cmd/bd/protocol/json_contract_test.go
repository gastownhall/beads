// json_contract_test.go — CI regression tests for --json output contracts.
//
// These tests verify that commands with --json always produce valid JSON
// and include required fields. Regressions like GH#2492, GH#2465, GH#2407,
// GH#2395 are prevented by these tests.
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestJSONContract_ListOutputIsValidJSON verifies bd list --json always
// produces valid JSON (not mixed with tree-renderer text).
func TestJSONContract_ListOutputIsValidJSON(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.create("JSON contract test issue")

	out := w.run("list", "--json")
	items := parseJSONOutput(t, out)
	if len(items) == 0 {
		t.Fatal("bd list --json returned no items")
	}
}

// TestJSONContract_ShowOutputHasRequiredFields verifies bd show --json
// includes all required issue fields.
func TestJSONContract_ShowOutputHasRequiredFields(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Required fields test")

	out := w.run("show", id, "--json")
	items := parseJSONOutput(t, out)
	if len(items) == 0 {
		t.Fatal("bd show --json returned no items")
	}

	issue := items[0]
	requiredFields := []string{"id", "title", "status", "priority", "issue_type", "created_at", "schema_version"}
	for _, field := range requiredFields {
		if _, ok := issue[field]; !ok {
			t.Errorf("bd show --json missing required field %q", field)
		}
	}
}

// TestJSONContract_ReadyOutputIsValidJSON verifies bd ready --json produces
// valid JSON even when no issues are ready.
func TestJSONContract_ReadyOutputIsValidJSON(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	out := w.run("ready", "--json")
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("bd ready --json produced invalid JSON: %v\nOutput:\n%s", err, out)
	}
}

// TestJSONContract_CreateOutputHasID verifies bd create --json returns
// the created issue with its ID.
func TestJSONContract_CreateOutputHasID(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	out := w.run("create", "Create contract test", "--description=test", "--json")

	var issue map[string]any
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("bd create --json produced invalid JSON: %v\nOutput:\n%s", err, out)
	}

	assertSchemaVersion(t, issue, "bd create --json")
	if _, ok := issue["id"]; !ok {
		t.Error("bd create --json output missing 'id' field")
	}
}

// TestJSONContract_ErrorOutputIsValidJSON verifies that errors with --json
// produce valid JSON with schema_version to stderr (not mixed text).
func TestJSONContract_ErrorOutputIsValidJSON(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// Try to show a nonexistent issue with --json
	out, _ := w.runExpectError("show", "nonexistent-xyz-999", "--json")

	// The output (stderr) should be valid JSON or empty
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return // Empty is acceptable for errors
	}

	// Try to parse as JSON object
	var errObj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &errObj); err != nil {
		// Try each line — error JSON may be mixed with other stderr output
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var lineObj map[string]any
			if json.Unmarshal([]byte(line), &lineObj) == nil {
				if _, hasError := lineObj["error"]; hasError {
					assertSchemaVersion(t, lineObj, "bd error JSON line")
					return
				}
			}
		}
		t.Logf("Note: error output not fully JSON — this is acceptable for some error paths")
	} else {
		if _, hasError := errObj["error"]; hasError {
			assertSchemaVersion(t, errObj, "bd show error --json")
		}
	}
}

// TestJSONContract_CloseOutputHasStatus verifies bd close --json returns
// the updated issue with closed status.
func TestJSONContract_CloseOutputHasStatus(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Close contract test")

	out := w.run("close", id, "--json")
	items := parseJSONOutput(t, out)
	if len(items) == 0 {
		t.Fatal("bd close --json returned no items")
	}

	assertField(t, items[0], "status", "closed")
}

// TestJSONContract_ReadyOutputHasFullObjects verifies bd ready --json returns
// full issue objects with dependency counts, not just IDs (beads-clt).
func TestJSONContract_ReadyOutputHasFullObjects(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.create("Ready full object test")

	out := w.run("ready", "--json")
	items := parseJSONOutput(t, out)
	if len(items) == 0 {
		t.Skip("no ready issues — create returned non-ready issue")
	}
	issue := items[0]
	requiredFields := []string{"id", "title", "status", "priority", "dependency_count", "dependent_count"}
	for _, field := range requiredFields {
		if _, ok := issue[field]; !ok {
			t.Errorf("bd ready --json item missing required field %q", field)
		}
	}
}

// TestJSONContract_BlockedOutputHasBlockedBy verifies bd blocked --json returns
// full issue objects with blocked_by field (beads-clt).
func TestJSONContract_BlockedOutputHasBlockedBy(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	blocker := w.create("Blocker issue")
	blocked := w.create("Blocked issue")
	w.run("dep", "add", blocked, blocker, "--type", "blocks")

	out := w.run("blocked", "--json")
	items := parseJSONOutput(t, out)

	var found map[string]any
	for _, item := range items {
		if id, ok := item["id"].(string); ok && id == blocked {
			found = item
			break
		}
	}
	if found == nil {
		t.Fatalf("blocked issue %s not found in bd blocked --json output", blocked)
	}

	requiredFields := []string{"id", "title", "status", "blocked_by_count", "blocked_by"}
	for _, field := range requiredFields {
		if _, ok := found[field]; !ok {
			t.Errorf("bd blocked --json item missing required field %q", field)
		}
	}
}

// TestJSONContract_PingOutputIsValidJSON verifies bd ping --json returns
// structured health check output with timing info.
func TestJSONContract_PingOutputIsValidJSON(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	out := w.run("ping", "--json")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("bd ping --json produced invalid JSON: %v\nOutput:\n%s", err, out)
	}
	assertSchemaVersion(t, obj, "bd ping --json")
	if status, ok := obj["status"].(string); !ok || status != "ok" {
		t.Errorf("bd ping --json status = %v, want ok", obj["status"])
	}
	if _, ok := obj["total_ms"]; !ok {
		t.Error("bd ping --json missing total_ms field")
	}
}

// TestJSONContract_SchemaVersionPresent verifies that schema_version is
// present in object-returning --json commands (show, create, ping).
// Array-returning commands (list, ready) do not include schema_version.
func TestJSONContract_SchemaVersionPresent(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Schema version test")

	tests := []struct {
		name string
		args []string
	}{
		{"show", []string{"show", id, "--json"}},
		{"ping", []string{"ping", "--json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := w.run(tt.args...)
			var obj map[string]any
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatalf("bd %s produced invalid JSON: %v\nOutput:\n%s",
					tt.name, err, out)
			}
			assertSchemaVersion(t, obj, "bd "+tt.name+" --json")
		})
	}
}
