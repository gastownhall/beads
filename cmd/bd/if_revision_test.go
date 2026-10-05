package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// lastJSONLine returns the final non-empty line of out, which is where
// reportIfRevisionFailure writes its machine body (the human line comes
// first; see reportIfRevisionFailure's own doc comment).
func lastJSONLine(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	t.Fatalf("no non-empty line in output:\n%s", out)
	return ""
}

// decodeIfRevisionBody decodes line as either the flat shape (schema_version
// beside the body's own fields) or the BD_JSON_ENVELOPE=1 shape
// ({"schema_version":N,"data":{...}}), returning the body fields as a plain
// map so a test can assert JSON TYPES (float64 for a number, never a string)
// without fighting a typed struct's own unmarshal coercion.
func decodeIfRevisionBody(t *testing.T, line string, enveloped bool) map[string]interface{} {
	t.Helper()
	var whole map[string]interface{}
	if err := json.Unmarshal([]byte(line), &whole); err != nil {
		t.Fatalf("decode JSON line: %v\nline: %s", err, line)
	}
	if !enveloped {
		return whole
	}
	data, ok := whole["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("enveloped line has no \"data\" object: %s", line)
	}
	return data
}

// TestIfRevisionEnvelopeGascityContract pins T4.3: the stderr JSON a refused
// --if-revision write emits, in both the flat and the BD_JSON_ENVELOPE=1
// shapes, must carry integer revisions and a `code` field exactly as
// gascity's bdstore_conditional.go classifier decodes them. A string
// revision -- the most tempting "it's just a big number" mistake -- makes
// gascity's json.Decoder fail closed on the WHOLE object
// (bdstore_conditional.go:134-139), discarding even the `code` beside it.
func TestIfRevisionEnvelopeGascityContract(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	jsonOutput = true

	runCase := func(t *testing.T, enveloped bool) {
		t.Helper()
		oldEnv, had := os.LookupEnv("BD_JSON_ENVELOPE")
		t.Cleanup(func() {
			if had {
				os.Setenv("BD_JSON_ENVELOPE", oldEnv)
			} else {
				os.Unsetenv("BD_JSON_ENVELOPE")
			}
		})
		if enveloped {
			os.Setenv("BD_JSON_ENVELOPE", "1")
		} else {
			os.Unsetenv("BD_JSON_ENVELOPE")
		}

		t.Run("stale_revision_carries_integer_expected_and_current", func(t *testing.T) {
			var reported error
			out := captureStderr(t, func() {
				reported, _ = reportIfRevisionFailure("updating", "bd-1", &issueops.VersionMismatchError{Expected: 5, Current: 7}, nil)
			})
			ee, ok := reported.(*exitError)
			if !ok || ee.Code != ExitGuardMismatch {
				t.Fatalf("reported error = %#v, want *exitError{Code: %d}", reported, ExitGuardMismatch)
			}
			if !strings.Contains(out, "precondition failed") {
				t.Errorf("human line must contain gascity's code-less-fallback phrase \"precondition failed\", got:\n%s", out)
			}
			body := decodeIfRevisionBody(t, lastJSONLine(t, out), enveloped)
			if body["code"] != ifRevisionCodePreconditionFailed {
				t.Errorf("code = %v, want %q", body["code"], ifRevisionCodePreconditionFailed)
			}
			if body["id"] != "bd-1" {
				t.Errorf("id = %v, want bd-1", body["id"])
			}
			expected, ok := body["expected_revision"].(float64)
			if !ok {
				t.Fatalf("expected_revision is %T (%v), want a JSON number -- a string here makes gascity's decoder fail closed on the whole object", body["expected_revision"], body["expected_revision"])
			}
			if expected != 5 {
				t.Errorf("expected_revision = %v, want 5", expected)
			}
			current, ok := body["current_revision"].(float64)
			if !ok {
				t.Fatalf("current_revision is %T (%v), want a JSON number", body["current_revision"], body["current_revision"])
			}
			if current != 7 {
				t.Errorf("current_revision = %v, want 7", current)
			}
			// The raw bytes must never show a quoted number either -- a
			// correct float64 decode does not rule out "5" (json.Unmarshal
			// happily coerces neither direction here, but a regression that
			// switched the struct field to string would still decode into
			// interface{} as a string, which the type assertion above already
			// catches; this is the belt to that suspenders).
			if strings.Contains(lastJSONLine(t, out), `"expected_revision":"`) {
				t.Errorf("expected_revision is quoted in the raw JSON, want a bare integer:\n%s", out)
			}
		})

		t.Run("stale_assignee_or_status_carries_no_revision_fields", func(t *testing.T) {
			var reported error
			out := captureStderr(t, func() {
				reported, _ = reportIfRevisionFailure("updating", "bd-2", storage.ErrAssigneeMismatch, nil)
			})
			ee, ok := reported.(*exitError)
			if !ok || ee.Code != ExitGuardMismatch {
				t.Fatalf("reported error = %#v, want *exitError{Code: %d}", reported, ExitGuardMismatch)
			}
			body := decodeIfRevisionBody(t, lastJSONLine(t, out), enveloped)
			if body["code"] != ifRevisionCodePreconditionFailed {
				t.Errorf("code = %v, want %q", body["code"], ifRevisionCodePreconditionFailed)
			}
			if _, present := body["expected_revision"]; present {
				t.Errorf("expected_revision must be absent (omitempty) for a non-revision guard, got %v", body["expected_revision"])
			}
			if _, present := body["current_revision"]; present {
				t.Errorf("current_revision must be absent (omitempty) for a non-revision guard, got %v", body["current_revision"])
			}
		})

		t.Run("schema_version_is_present_and_an_integer", func(t *testing.T) {
			out := captureStderr(t, func() {
				_, _ = reportIfRevisionFailure("updating", "bd-3", &issueops.VersionMismatchError{Expected: 1, Current: 2}, nil)
			})
			var whole map[string]interface{}
			if err := json.Unmarshal([]byte(lastJSONLine(t, out)), &whole); err != nil {
				t.Fatalf("decode: %v", err)
			}
			sv, ok := whole["schema_version"].(float64)
			if !ok || sv != float64(JSONSchemaVersion) {
				t.Errorf("schema_version = %v, want %d", whole["schema_version"], JSONSchemaVersion)
			}
		})
	}

	t.Run("flat", func(t *testing.T) { runCase(t, false) })
	t.Run("enveloped_BD_JSON_ENVELOPE", func(t *testing.T) { runCase(t, true) })
}

// TestIfRevisionUnsupportedBackendCode pins T4.9: a backend whose Lifecycle
// cannot honor ExpectedVersion at all (issueops.ErrUnsupported) must report
// "conditional_write_unsupported" and exit 1 -- never ExitGuardMismatch,
// which promises callers "a racer won, do not retry", and never a silent
// unconditional fallback that drops the guard on the floor.
func TestIfRevisionUnsupportedBackendCode(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	jsonOutput = true

	var reported error
	var ok bool
	out := captureStderr(t, func() {
		reported, ok = reportIfRevisionFailure("deleting", "bd-9", &issueops.ErrUnsupported{Op: "Delete.ExpectedVersion", Backend: "stub"}, nil)
	})
	if !ok {
		t.Fatalf("reportIfRevisionFailure did not recognize issueops.ErrUnsupported as a guard outcome")
	}
	ee, isExitErr := reported.(*exitError)
	if !isExitErr || ee.Code != 1 {
		t.Fatalf("reported error = %#v, want *exitError{Code: 1}", reported)
	}
	if !strings.Contains(out, "conditional write unsupported") {
		t.Errorf("human line should say the write is unsupported, got:\n%s", out)
	}
	body := decodeIfRevisionBody(t, lastJSONLine(t, out), false)
	if body["code"] != ifRevisionCodeUnsupported {
		t.Errorf("code = %v, want %q", body["code"], ifRevisionCodeUnsupported)
	}
	if _, present := body["expected_revision"]; present {
		t.Errorf("expected_revision must be absent for an unsupported-backend refusal, got %v", body["expected_revision"])
	}

	// classifyIfRevisionFailure is reportIfRevisionFailure's own classifier;
	// pinned directly too so a future refactor that bypasses the report
	// function still has this assertion on the mapping itself.
	code, _, expected, current, classified := classifyIfRevisionFailure(&issueops.ErrUnsupported{Op: "x", Backend: "y"}, nil)
	if !classified || code != ifRevisionCodeUnsupported {
		t.Errorf("classifyIfRevisionFailure(ErrUnsupported) = %q, %v, want %q, true", code, classified, ifRevisionCodeUnsupported)
	}
	if expected != nil || current != nil {
		t.Errorf("classifyIfRevisionFailure(ErrUnsupported) revisions = %v, %v, want nil, nil", expected, current)
	}
}

// TestClassifyIfRevisionFailureUnrelatedError pins ok=false for an error that
// is neither a guard outcome nor an unsupported-backend signal: the caller's
// own (unrelated) failure handling must run unchanged, and nothing here may
// claim the error as its own.
func TestClassifyIfRevisionFailureUnrelatedError(t *testing.T) {
	if _, _, _, _, ok := classifyIfRevisionFailure(nil, nil); ok {
		t.Errorf("classifyIfRevisionFailure(nil) ok = true, want false")
	}
	if _, _, _, _, ok := classifyIfRevisionFailure(errUnrelatedForIfRevisionTest, nil); ok {
		t.Errorf("classifyIfRevisionFailure(unrelated error) ok = true, want false")
	}
	if reported, ok := reportIfRevisionFailure("updating", "bd-4", errUnrelatedForIfRevisionTest, nil); ok || reported != nil {
		t.Errorf("reportIfRevisionFailure(unrelated error) = %v, %v, want nil, false", reported, ok)
	}
}

var errUnrelatedForIfRevisionTest = errors.New("boom: unrelated failure")

// TestClassifyIfRevisionFailureBareVersionMismatchSentinel pins mc-zndi7.78:
// an HTTP-backed store (bd-enterprise, after the gascity sync) that returns
// the bare storage.ErrVersionMismatch sentinel -- not the typed
// *issueops.VersionMismatchError -- must still classify as
// "precondition_failed" / ExitGuardMismatch, falling back to the caller's own
// ifRevision for expected_revision (since the sentinel itself carries none)
// and omitting current_revision (genuinely unknown).
func TestClassifyIfRevisionFailureBareVersionMismatchSentinel(t *testing.T) {
	ifRevision := int64(42)
	code, _, expected, current, ok := classifyIfRevisionFailure(storage.ErrVersionMismatch, &ifRevision)
	if !ok || code != ifRevisionCodePreconditionFailed {
		t.Fatalf("classifyIfRevisionFailure(bare ErrVersionMismatch) = %q, %v, want %q, true", code, ok, ifRevisionCodePreconditionFailed)
	}
	if expected == nil || *expected != ifRevision {
		t.Errorf("expected_revision = %v, want &%d (the caller's --if-revision value)", expected, ifRevision)
	}
	if current != nil {
		t.Errorf("current_revision = %v, want nil (unknown to the bare sentinel)", current)
	}

	reported, ok := reportIfRevisionFailure("updating", "bd-5", storage.ErrVersionMismatch, &ifRevision)
	if !ok {
		t.Fatalf("reportIfRevisionFailure did not recognize the bare ErrVersionMismatch sentinel as a guard outcome")
	}
	ee, isExitErr := reported.(*exitError)
	if !isExitErr || ee.Code != ExitGuardMismatch {
		t.Fatalf("reported error = %#v, want *exitError{Code: %d}", reported, ExitGuardMismatch)
	}
}

// TestClassifyIfRevisionFailureNotFoundSentinel pins mc-zndi7.81: a pre-flight
// existence check that fails with the bare storage.ErrNotFound sentinel --
// e.g. cmd/bd/delete.go's resolveAndGetIssueForMutation call, which runs
// before deleter.Delete() and the per-id lock fence #7244 added, so a
// same-token --if-revision racer that loses that fence can find the row
// already gone right there -- must classify exactly like a mid-guard version
// mismatch: "precondition_failed" / ExitGuardMismatch, falling back to the
// caller's own --if-revision value for expected_revision and omitting
// current_revision (the row is gone; there is nothing left to report).
// Without this, that pre-flight path surfaces an unclassified, uncoded exit 1
// instead of joining every other --if-revision loser at exit 13.
func TestClassifyIfRevisionFailureNotFoundSentinel(t *testing.T) {
	ifRevision := int64(7)
	code, _, expected, current, ok := classifyIfRevisionFailure(storage.ErrNotFound, &ifRevision)
	if !ok || code != ifRevisionCodePreconditionFailed {
		t.Fatalf("classifyIfRevisionFailure(ErrNotFound) = %q, %v, want %q, true", code, ok, ifRevisionCodePreconditionFailed)
	}
	if expected == nil || *expected != ifRevision {
		t.Errorf("expected_revision = %v, want &%d (the caller's --if-revision value)", expected, ifRevision)
	}
	if current != nil {
		t.Errorf("current_revision = %v, want nil (the row is gone; nothing to report)", current)
	}

	reported, ok := reportIfRevisionFailure("deleting", "bd-6", storage.ErrNotFound, &ifRevision)
	if !ok {
		t.Fatalf("reportIfRevisionFailure did not recognize storage.ErrNotFound as a guard outcome")
	}
	ee, isExitErr := reported.(*exitError)
	if !isExitErr || ee.Code != ExitGuardMismatch {
		t.Fatalf("reported error = %#v, want *exitError{Code: %d}", reported, ExitGuardMismatch)
	}
}

// TestClassifyIfRevisionFailureTypedBeforeSentinel pins ordering: a typed
// *issueops.VersionMismatchError (which also satisfies errors.Is against the
// same ErrVersionMismatch sentinel via Unwrap) must still report its OWN
// Expected/Current, not fall into the bare-sentinel branch and report the
// caller's ifRevision/nil instead.
func TestClassifyIfRevisionFailureTypedBeforeSentinel(t *testing.T) {
	ifRevision := int64(999)
	code, _, expected, current, ok := classifyIfRevisionFailure(&issueops.VersionMismatchError{Expected: 5, Current: 7}, &ifRevision)
	if !ok || code != ifRevisionCodePreconditionFailed {
		t.Fatalf("classifyIfRevisionFailure(typed) = %q, %v, want %q, true", code, ok, ifRevisionCodePreconditionFailed)
	}
	if expected == nil || *expected != 5 {
		t.Errorf("expected_revision = %v, want &5 (the typed error's own Expected, not ifRevision)", expected)
	}
	if current == nil || *current != 7 {
		t.Errorf("current_revision = %v, want &7 (the typed error's own Current)", current)
	}
}
