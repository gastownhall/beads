package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestRefuseUnverifiablePreviewOpenRendersTypedErrors pins the regression the
// identity gate introduced by refusing on its own generic wrapper.
//
// The preview open runs CheckForwardDrift, so a schema-ahead database — the
// recurring stale-binary class — fails the PREVIEW now, not the real open. On
// base that class surfaced from the real open and was rendered by
// renderTypedOpenError with SchemaSkewError.UserMessage()'s actionable rebuild
// block, and with a JSON body under --json. A bare HandleError on the gate's
// refusal arm replaced both with an unstructured line, and appended a
// BEADS_SKIP_IDENTITY_CHECK=1 hint that cannot help: skipping the gate just
// makes the real open fail on the identical skew.
func TestRefuseUnverifiablePreviewOpenRendersTypedErrors(t *testing.T) {
	skew := &schema.SchemaSkewError{DBVersion: 71, BinaryVersion: 68}

	t.Run("schema skew keeps its own UX, not the generic wrapper", func(t *testing.T) {
		oldJSON := jsonOutput
		jsonOutput = false
		t.Cleanup(func() { jsonOutput = oldJSON })

		var err error
		out := captureStderr(t, func() { err = refuseUnverifiablePreviewOpen(skew) })

		if err == nil {
			t.Fatal("refusal returned nil; the gate must still fail the command")
		}
		if strings.Contains(out, "could not verify workspace identity") {
			t.Errorf("schema skew was rendered as the generic identity wrapper:\n%s", out)
		}
		if strings.Contains(out, "BEADS_SKIP_IDENTITY_CHECK") {
			t.Errorf("refusal advertised BEADS_SKIP_IDENTITY_CHECK for a skew, where skipping the gate leaves the real open failing identically:\n%s", out)
		}
		// The skew renderer's own text, not this test's paraphrase of it.
		if !strings.Contains(out, skew.Error()) {
			t.Errorf("stderr = %q, want the SchemaSkewError message %q", out, skew.Error())
		}
	})

	t.Run("schema skew under --json emits a JSON body", func(t *testing.T) {
		oldJSON := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = oldJSON })

		out := captureStderr(t, func() { _ = refuseUnverifiablePreviewOpen(skew) })

		var body map[string]interface{}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatalf("--json refusal did not emit a JSON body (%v); raw stderr:\n%s", err, out)
		}
		raw, ok := body["schema_skew"]
		if !ok {
			t.Fatalf("JSON body has no schema_skew object, so a machine caller cannot tell skew from any other failure: %v", body)
		}
		detail, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("schema_skew is %T, want an object", raw)
		}
		for key, want := range map[string]float64{
			"current_version":  71,
			"required_version": 68,
			"delta":            3,
		} {
			if got, _ := detail[key].(float64); got != want {
				t.Errorf("schema_skew[%q] = %v, want %v", key, detail[key], want)
			}
		}
	})

	t.Run("a preview-specific error keeps the generic message and respects --json", func(t *testing.T) {
		// Not a typed open error: this is the class the generic arm is for, and
		// the only class where the override hint is honest.
		previewErr := fmt.Errorf("dial tcp 127.0.0.1:3306: %w", errors.New("connection refused"))

		oldJSON := jsonOutput
		jsonOutput = false
		t.Cleanup(func() { jsonOutput = oldJSON })

		plain := captureStderr(t, func() { _ = refuseUnverifiablePreviewOpen(previewErr) })
		if !strings.Contains(plain, "could not verify workspace identity") {
			t.Errorf("stderr = %q, want the generic identity refusal", plain)
		}
		if !strings.Contains(plain, "BEADS_SKIP_IDENTITY_CHECK") {
			t.Errorf("stderr = %q, want the override hint on the arm where it actually applies", plain)
		}

		// The generic arm's JSON body goes to STDOUT (HandleErrorRespectJSON →
		// jsonStdoutError), unlike handleSchemaSkewJSON's, which goes to stderr.
		// That split is pre-existing; what matters for this finding is that a
		// --json caller receives a parseable body on one of them instead of the
		// bare stderr line the gate used to emit.
		jsonOutput = true
		// captureStdout fails the test on a returned error, and this refusal
		// returns a non-nil exitError by design, so record it separately.
		var refusal error
		structured := captureStdout(t, func() error {
			refusal = refuseUnverifiablePreviewOpen(previewErr)
			return nil
		})
		if refusal == nil {
			t.Error("refusal returned nil under --json; the command must still fail")
		}
		var body map[string]interface{}
		if err := json.Unmarshal([]byte(structured), &body); err != nil {
			t.Fatalf("--json generic refusal emitted no JSON body (%v); a machine caller gets exit 1 with nothing to parse:\n%s", err, structured)
		}
		if _, ok := body["error"]; !ok {
			t.Errorf("JSON body has no error field: %v", body)
		}
	})
}
