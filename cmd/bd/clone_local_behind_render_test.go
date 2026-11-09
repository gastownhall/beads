package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestRenderTypedOpenErrorCloneLocalBehind covers the CLI's rendering of the
// clone-local refusal a read-only open returns after the old-binary pull and
// upgrade (gastownhall/beads#7038). It reaches renderTypedOpenError wrapped,
// as a refused store open does, and must come out as the full guidance block
// on a human run and as one valid JSON error carrying the remedy under
// --json, never as a one-line `%v` of the raw error.
func TestRenderTypedOpenErrorCloneLocalBehind(t *testing.T) {
	orig := globalFlag
	t.Cleanup(func() { globalFlag = orig })
	globalFlag = false

	direct := func() *schema.CloneLocalBehindError {
		return &schema.CloneLocalBehindError{
			IgnoredVersion: 11, LatestIgnoredVersion: 31,
			Table: "leases", MissingColumns: []string{"granted_node"},
			MainVersion: 74, BinaryVersion: 74,
		}
	}
	render := func(t *testing.T, refusal *schema.CloneLocalBehindError) string {
		t.Helper()
		var handled bool
		out := captureNoticeStderr(t, func() {
			handled = renderTypedOpenError(fmt.Errorf("failed to open database: %w", refusal))
		})
		if !handled {
			t.Fatal("renderTypedOpenError did not handle a wrapped CloneLocalBehindError")
		}
		return out
	}

	t.Run("human", func(t *testing.T) {
		pinJSONOutput(t, false)
		refusal := direct()
		out := render(t, refusal)
		if out != refusal.UserMessage() {
			t.Errorf("human refusal is not the UserMessage block:\n%s", out)
		}
		for _, want := range []string{"granted_node", "bd dolt pull", "bd migrate schema", "no further main-schema"} {
			if !strings.Contains(out, want) {
				t.Errorf("human refusal does not mention %q:\n%s", want, out)
			}
		}
	})

	t.Run("human main behind too", func(t *testing.T) {
		pinJSONOutput(t, false)
		refusal := direct()
		refusal.MainVersion = 72
		out := render(t, refusal)
		if !strings.Contains(out, "designated-migrator") || strings.Contains(out, "no further main-schema") {
			t.Errorf("a main series behind too must send the operator through the migrator procedure first, not claim main is current:\n%s", out)
		}
	})

	t.Run("human --global", func(t *testing.T) {
		pinJSONOutput(t, false)
		globalFlag = true
		t.Cleanup(func() { globalFlag = false })
		out := render(t, direct())
		if !strings.Contains(out, schema.SharedConsentCommandGlobal) {
			t.Errorf("under --global the refusal must name %q:\n%s", schema.SharedConsentCommandGlobal, out)
		}
	})

	t.Run("json", func(t *testing.T) {
		pinJSONOutput(t, true)
		t.Setenv("BD_JSON_ENVELOPE", "")
		out := render(t, direct())
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("--json refusal is not one JSON document: %v\n%s", err, out)
		}
		if hint, _ := parsed["hint"].(string); !strings.Contains(hint, "bd migrate schema") {
			t.Errorf("hint = %q, want the per-database remedy", hint)
		}
		detail, ok := parsed["clone_local_schema"].(map[string]interface{})
		if !ok {
			t.Fatalf("clone_local_schema missing or wrong type: %T\n%s", parsed["clone_local_schema"], out)
		}
		if detail["command"] != "bd migrate schema" || detail["gateway"] != false ||
			detail["table"] != "leases" || fmt.Sprint(detail["missing_columns"]) != "[granted_node]" {
			t.Errorf("clone_local_schema = %v", detail)
		}
	})

	t.Run("gateway", func(t *testing.T) {
		for _, jsonMode := range []bool{false, true} {
			pinJSONOutput(t, jsonMode)
			t.Setenv("BD_JSON_ENVELOPE", "")
			refusal := direct()
			refusal.Gateway = true
			out := render(t, refusal)
			if !strings.Contains(out, "server administrator") || strings.Contains(out, "bd migrate schema") {
				t.Errorf("gateway refusal (json=%v) must send the operator to the server administrator, not to `bd migrate schema`:\n%s", jsonMode, out)
			}
			if jsonMode {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(out), &parsed); err != nil {
					t.Fatalf("--json gateway refusal is not one JSON document: %v\n%s", err, out)
				}
				if _, has := parsed["clone_local_schema"].(map[string]interface{})["command"]; has {
					t.Errorf("gateway JSON refusal names a command: %s", out)
				}
			}
		}
	})
}
