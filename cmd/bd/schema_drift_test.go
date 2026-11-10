package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// driftTestRefusal is the typed refusal the rendering tests feed through the
// same wrapping the real open/migrate paths apply, with a cause that carries
// a multi-line server stack the bounded symptom must strip.
func driftTestRefusal() *schema.SchemaEncodingDriftError {
	return &schema.SchemaEncodingDriftError{
		Migration:   "0059_recompute_null_gate_is_blocked.up.sql",
		Version:     59,
		CursorTable: "schema_migrations",
		Cause:       errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19\ngoroutine 1 [running]:\nsome.Frame(...)"),
	}
}

// TestSchemaEncodingDriftJSON pins the --json rendering of the drift refusal
// (gastownhall/beads#5699): the standard error envelope with a
// schema_encoding_drift object carrying the migration context, the bounded
// single-line symptom (no engine stack), and the recovery URL, plus a hint
// that points at operator-supervised recovery and names no bypass flag.
func TestSchemaEncodingDriftJSON(t *testing.T) {
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()

	drift := driftTestRefusal()
	out := captureStderr(t, func() {
		if !renderSchemaEncodingDriftRefusal(fmt.Errorf("open store: %w", drift)) {
			t.Error("renderSchemaEncodingDriftRefusal = false for a wrapped drift refusal")
		}
	})

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, out)
	}
	if got, ok := parsed["error"].(string); !ok || !strings.Contains(got, "schema encoding drift") || !strings.Contains(got, "0059") {
		t.Errorf("error = %v, want the drift one-liner naming 0059", parsed["error"])
	}
	if got, ok := parsed["hint"].(string); !ok || !strings.Contains(got, "operator-supervised") {
		t.Errorf("hint = %v, want the operator-supervised recovery directive", parsed["hint"])
	} else if strings.Contains(got, "BD_ALLOW") || strings.Contains(got, "--force") {
		t.Errorf("hint must not name a bypass flag: %q", got)
	}

	obj, ok := parsed["schema_encoding_drift"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema_encoding_drift key missing or wrong type: %T", parsed["schema_encoding_drift"])
	}
	if got := obj["migration"]; got != drift.Migration {
		t.Errorf("schema_encoding_drift.migration = %v, want %q", got, drift.Migration)
	}
	if got := obj["version"]; got != float64(59) {
		t.Errorf("schema_encoding_drift.version = %v, want 59", got)
	}
	if got := obj["cursor_table"]; got != "schema_migrations" {
		t.Errorf("schema_encoding_drift.cursor_table = %v, want %q", got, "schema_migrations")
	}
	symptom, _ := obj["symptom"].(string)
	if !strings.Contains(symptom, "invalid hash length: 19") {
		t.Errorf("schema_encoding_drift.symptom = %q, want the engine's drift line", symptom)
	}
	if strings.Contains(symptom, "\n") || strings.Contains(symptom, "goroutine") {
		t.Errorf("schema_encoding_drift.symptom must be a single line without the engine stack: %q", symptom)
	}
	if got := obj["recovery_url"]; got != schema.SchemaEncodingDriftRecoveryURL {
		t.Errorf("schema_encoding_drift.recovery_url = %v, want %q", got, schema.SchemaEncodingDriftRecoveryURL)
	}
}

// TestSchemaEncodingDriftMigrateRendering pins the explicit-migration wiring
// (bd migrate schema): the drift refusal renders through the same surface
// the open paths use, terminates with the normal failure exit 1, and prints
// exactly one refusal with no duplicate generic error on top. An unrelated
// failure keeps the generic handling.
func TestSchemaEncodingDriftMigrateRendering(t *testing.T) {
	t.Run("text mode: full refusal, exit 1, no duplicate", func(t *testing.T) {
		origJSON := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = origJSON }()

		drift := driftTestRefusal()
		var err error
		out := captureStderr(t, func() { err = handleSchemaMigrateFailure(fmt.Errorf("migrate: %w", drift)) })
		if err == nil {
			t.Fatal("handleSchemaMigrateFailure returned nil for a drift refusal")
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Errorf("exit code = %d, want the normal failure 1", code)
		}
		if out != drift.UserMessage() {
			t.Errorf("output is not exactly the UserMessage block:\n%s", out)
		}
		if got := strings.Count(out, schema.SchemaEncodingDriftRecoveryURL); got != 1 {
			t.Errorf("recovery URL appears %d times, want exactly 1 (no duplicate refusal)", got)
		}
		if strings.Contains(out, "Error: schema migration failed") {
			t.Errorf("generic failure line printed on top of the handled refusal:\n%s", out)
		}
	})

	t.Run("json mode: envelope with the drift object, exit 1", func(t *testing.T) {
		origJSON := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = origJSON }()

		drift := driftTestRefusal()
		var err error
		out := captureStderr(t, func() { err = handleSchemaMigrateFailure(fmt.Errorf("migrate: %w", drift)) })
		if err == nil {
			t.Fatal("handleSchemaMigrateFailure returned nil for a drift refusal")
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Errorf("exit code = %d, want the normal failure 1", code)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, out)
		}
		obj, ok := parsed["schema_encoding_drift"].(map[string]interface{})
		if !ok {
			t.Fatalf("schema_encoding_drift key missing from the migrate refusal: %s", out)
		}
		if got := obj["migration"]; got != drift.Migration {
			t.Errorf("schema_encoding_drift.migration = %v, want %q", got, drift.Migration)
		}
		// Exactly one refusal: the envelope decodes as a single JSON object
		// carrying one schema_encoding_drift object, with no plain-text line
		// after it. (The URL itself appears in both the error one-liner and
		// the structured recovery_url field — that is the envelope, not a
		// duplicate refusal.)
		if got := strings.Count(out, "\"schema_encoding_drift\""); got != 1 {
			t.Errorf("schema_encoding_drift object appears %d times, want exactly 1:\n%s", got, out)
		}
	})

	t.Run("an unrelated failure keeps the generic handling", func(t *testing.T) {
		origJSON := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = origJSON }()

		var err error
		out := captureStderr(t, func() { err = handleSchemaMigrateFailure(errors.New("connection refused")) })
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(out, "Error: schema migration failed: connection refused") {
			t.Errorf("generic handling changed:\n%s", out)
		}
	})
}
