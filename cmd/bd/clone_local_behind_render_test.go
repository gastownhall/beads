package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestRenderTypedOpenErrorCloneLocalBehind covers renderTypedOpenError's
// clone-local arm in-process. The refusal reaches it wrapped, as a refused
// store open does, and must come out as the full guidance block on a human
// run (plus the --global verb when the invocation targeted the global
// database) and as one valid JSON error carrying the remedy under --json,
// never as a one-line `%v` of the raw error. Under BD_JSON_ENVELOPE the typed
// block sits inside data beside error and hint, so an agent that unwraps the
// envelope keeps it. A gateway refusal sends the operator to the server
// administrator and names no command on either arm.
//
// Kept apart from the cgo-only clone_local_behind_test.go and free of any
// Dolt fixture, so it builds and runs without cgo too;
// TestCloneLocalRefusalRendering drives the same arm through a real refused
// open.
func TestRenderTypedOpenErrorCloneLocalBehind(t *testing.T) {
	orig := globalFlag
	t.Cleanup(func() { globalFlag = orig })
	globalFlag = false

	direct := func() *schema.CloneLocalBehindError {
		return &schema.CloneLocalBehindError{
			DBIgnoredVersion: 11, LatestIgnoredVersion: 31,
			MissingColumns: []string{"leases.granted_node"},
			MainVersion:    74, LatestMainVersion: 74,
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
	// payloadOf decodes a --json refusal and returns the object that carries
	// error, hint and the typed block: the document itself, or its data
	// object under BD_JSON_ENVELOPE.
	payloadOf := func(t *testing.T, out string, envelope bool) map[string]interface{} {
		t.Helper()
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("--json refusal is not one JSON document: %v\n%s", err, out)
		}
		if !envelope {
			return doc
		}
		if _, outside := doc["clone_local_schema"]; outside {
			t.Errorf("enveloped refusal carries clone_local_schema outside data, where an agent that unwraps the envelope loses it:\n%s", out)
		}
		data, ok := doc["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("enveloped refusal has no data object:\n%s", out)
		}
		return data
	}
	pinEnvelope := func(t *testing.T, envelope bool) {
		t.Helper()
		if envelope {
			t.Setenv("BD_JSON_ENVELOPE", "1")
		} else {
			t.Setenv("BD_JSON_ENVELOPE", "")
		}
	}

	t.Run("human", func(t *testing.T) {
		pinJSONOutput(t, false)
		refusal := direct()
		out := render(t, refusal)
		if out != refusal.UserMessage() {
			t.Errorf("human refusal is not the UserMessage block:\n%s", out)
		}
		for _, want := range []string{"leases.granted_node", "bd dolt pull", "bd migrate schema", "no further shared-schema"} {
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
		if !strings.Contains(out, "designated-migrator") || strings.Contains(out, "no further shared-schema") {
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

	for _, envelope := range []bool{false, true} {
		t.Run(fmt.Sprintf("json envelope=%v", envelope), func(t *testing.T) {
			pinJSONOutput(t, true)
			pinEnvelope(t, envelope)
			out := render(t, direct())
			payload := payloadOf(t, out, envelope)
			if hint, _ := payload["hint"].(string); !strings.Contains(hint, "bd migrate schema") {
				t.Errorf("hint = %q, want the per-database remedy", hint)
			}
			detail, ok := payload["clone_local_schema"].(map[string]interface{})
			if !ok {
				t.Fatalf("clone_local_schema missing or wrong type: %T\n%s", payload["clone_local_schema"], out)
			}
			if detail["command"] != schema.SharedConsentCommand || detail["gateway"] != false ||
				fmt.Sprint(detail["missing_columns"]) != "[leases.granted_node]" {
				t.Errorf("clone_local_schema = %v", detail)
			}
		})

		t.Run(fmt.Sprintf("json --global envelope=%v", envelope), func(t *testing.T) {
			pinJSONOutput(t, true)
			pinEnvelope(t, envelope)
			globalFlag = true
			t.Cleanup(func() { globalFlag = false })
			out := render(t, direct())
			detail, _ := payloadOf(t, out, envelope)["clone_local_schema"].(map[string]interface{})
			if detail["command"] != schema.SharedConsentCommandGlobal {
				t.Errorf("clone_local_schema.command under --global = %v, want %q", detail["command"], schema.SharedConsentCommandGlobal)
			}
		})
	}

	t.Run("gateway", func(t *testing.T) {
		for _, mode := range []struct {
			json, envelope bool
		}{{false, false}, {true, false}, {true, true}} {
			pinJSONOutput(t, mode.json)
			pinEnvelope(t, mode.envelope)
			refusal := direct()
			refusal.Gateway = true
			out := render(t, refusal)
			if !strings.Contains(out, "server administrator") || strings.Contains(out, "bd migrate schema") {
				t.Errorf("gateway refusal (%+v) must send the operator to the server administrator, not to `bd migrate schema`:\n%s", mode, out)
			}
			if mode.json {
				detail, ok := payloadOf(t, out, mode.envelope)["clone_local_schema"].(map[string]interface{})
				if !ok {
					t.Fatalf("gateway JSON refusal (%+v) has no clone_local_schema block:\n%s", mode, out)
				}
				if _, has := detail["command"]; has {
					t.Errorf("gateway JSON refusal (%+v) names a command: %s", mode, out)
				}
			}
		}
	})
}
