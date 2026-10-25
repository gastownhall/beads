// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestWireRevisionUnsupportedReasonBuildsTheTypedError pins the per-request
// mirror of the handshake gate: a 400 invalid_argument whose reason is
// wire_revision_unsupported must decode to *WireRevisionUnsupportedError, not
// fall through to the generic ErrValidation the bare invalid_argument code
// would otherwise map to (reason is the only thing telling the two apart,
// exactly as it is for project_mismatch).
func TestWireRevisionUnsupportedReasonBuildsTheTypedError(t *testing.T) {
	body := `{"status":400,"code":"invalid_argument","reason":"wire_revision_unsupported",` +
		`"bd_version":"1.9.0","min_wire_revision":3,"wire_revision":3}`
	err := refuseWith(t, 400, body, nil, listIssues())

	if !errors.Is(err, ErrWireRevisionUnsupported) {
		t.Fatalf("err = %v, want it to wrap ErrWireRevisionUnsupported", err)
	}
	if errors.Is(err, issueops.ErrValidation) {
		t.Fatal("err also satisfies the generic ErrValidation; reason must take priority over the code table")
	}
	var wru *WireRevisionUnsupportedError
	if !errors.As(err, &wru) {
		t.Fatalf("err is %T, want *WireRevisionUnsupportedError", err)
	}
	if wru.BdVersion != "1.9.0" {
		t.Errorf("BdVersion = %q, want 1.9.0", wru.BdVersion)
	}
	if wru.MinClientWireRevision != 3 || wru.ServerWireRevision != 3 {
		t.Errorf("MinClientWireRevision=%d ServerWireRevision=%d, want 3 and 3", wru.MinClientWireRevision, wru.ServerWireRevision)
	}
	if wru.ClientWireRevision != ClientWireRevision {
		t.Errorf("ClientWireRevision = %d, want this build's own %d", wru.ClientWireRevision, ClientWireRevision)
	}
	// err.Error() renders the generic ProblemError text (operation/status/code);
	// the typed sentinel's own message — the one naming the bd_version and
	// revision numbers — only comes from calling Error() on the unwrapped
	// *WireRevisionUnsupportedError itself.
	msg := wru.Error()
	for _, want := range []string{"1.9.0", "3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("wru.Error() = %q, want %q in it", msg, want)
		}
	}
}

// TestWireRevisionUnsupportedReasonRidesTheGenericCode proves the dispatch key
// really is reason-before-code: the HTTP status and `code` here are the same
// ones an ordinary malformed-argument refusal would use.
func TestWireRevisionUnsupportedReasonRidesTheGenericCode(t *testing.T) {
	body := `{"status":400,"code":"invalid_argument","param":"not_this","reason":"wire_revision_unsupported"}`
	err := refuseWith(t, 400, body, nil, listIssues())
	var wru *WireRevisionUnsupportedError
	if !errors.As(err, &wru) {
		t.Fatalf("err is %T, want *WireRevisionUnsupportedError even though code is the generic invalid_argument", err)
	}
}

// TestLegacyRevisionTokensSurviveAsBareIntegers is the pre-#6053 tolerance:
// a server old enough to predate the decimal-string revision token spelling
// sends expected_version/actual_version as bare JSON integers. The generated
// Problem types them *string, so the primary decode silently drops a
// type-mismatched field (mapProblem discards json.Unmarshal's error) --
// legacyRevisionFields must recover it from the same bytes.
func TestLegacyRevisionTokensSurviveAsBareIntegers(t *testing.T) {
	body := `{"status":409,"code":"precondition_failed","param":"expected_version",` +
		`"expected_version":42,"actual_version":43}`
	err := refuseWith(t, 409, body, nil, listIssues())

	var p *ProblemError
	if !errors.As(err, &p) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Errorf("param expected_version did not map to ErrVersionMismatch: %v", err)
	}
	if p.ExpectedVersion == nil || *p.ExpectedVersion != "42" {
		t.Fatalf("ExpectedVersion = %s, want \"42\" recovered from the bare integer", renderVersion(p.ExpectedVersion))
	}
	if p.ActualVersion == nil || *p.ActualVersion != "43" {
		t.Fatalf("ActualVersion = %s, want \"43\" recovered from the bare integer", renderVersion(p.ActualVersion))
	}
}

// TestLegacyRevisionFieldsDoesNotOverrideAProperlyDecodedString proves the
// fallback is additive only: when the primary decode already produced a
// string (the shape every server since #6053 sends), legacyRevisionFields
// must not touch it.
func TestLegacyRevisionFieldsDoesNotOverrideAProperlyDecodedString(t *testing.T) {
	body := `{"status":409,"code":"precondition_failed","param":"expected_version",` +
		`"expected_version":"7","actual_version":"8"}`
	err := refuseWith(t, 409, body, nil, listIssues())

	var p *ProblemError
	if !errors.As(err, &p) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if p.ExpectedVersion == nil || *p.ExpectedVersion != "7" {
		t.Fatalf("ExpectedVersion = %s, want \"7\"", renderVersion(p.ExpectedVersion))
	}
	if p.ActualVersion == nil || *p.ActualVersion != "8" {
		t.Fatalf("ActualVersion = %s, want \"8\"", renderVersion(p.ActualVersion))
	}
}

// TestLegacyRevisionFieldsLeavesAbsenceAlone proves the fallback does not
// manufacture a value the server never sent: an operation that cannot report
// actual_version at all (the "an operation that cannot report what it found
// says so by absence" case the string-shape test already pins) must stay nil
// through the integer-tolerant path too.
func TestLegacyRevisionFieldsLeavesAbsenceAlone(t *testing.T) {
	body := `{"status":409,"code":"precondition_failed","param":"expected_version","expected_version":42}`
	err := refuseWith(t, 409, body, nil, listIssues())

	var p *ProblemError
	if !errors.As(err, &p) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if p.ExpectedVersion == nil || *p.ExpectedVersion != "42" {
		t.Fatalf("ExpectedVersion = %s, want \"42\"", renderVersion(p.ExpectedVersion))
	}
	if p.ActualVersion != nil {
		t.Errorf("ActualVersion = %q, want nil: the server reported none", *p.ActualVersion)
	}
}
