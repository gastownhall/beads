package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// The tests in this file cover the WIRING of the schema fix gate, not its
// predicate logic (that lives in cmd/bd/doctor/fix_gate_test.go).
//
// This distinction is the whole point. GH#4993 originally shipped with a
// correct predicate and a full suite of green tests, while three of the four
// output paths emitted unguarded advice, one command family bypassed the gate
// entirely, and a bare `bd doctor` applied migrations before the gate was
// consulted. Every one of those is a wiring defect, and a test that constructs
// a FixGate by hand cannot observe any of them.

const rawFixTip = "Run 'bd doctor --fix' to untrack runtime files"

// blockedGate is a determined gate that forbids recommending --fix.
func blockedGate() doctor.FixGate {
	return doctor.FixGate{
		Determined:    true,
		DBReachable:   true,
		Ahead:         true,
		AllowFSFix:    true,
		DBVersion:     52,
		BinaryVersion: 49,
		Reason:        "database schema is at v52, this binary knows up to v49 (3 migrations ahead)",
	}
}

func resultWithFixTip() doctorResult {
	return doctorResult{
		Path:       "/tmp/does-not-need-to-exist",
		CLIVersion: "test",
		Checks: []doctorCheck{
			{
				Name:     "Tracked Runtime Files",
				Status:   statusWarning,
				Message:  "runtime files are tracked",
				Category: "Core",
				Fix:      rawFixTip,
			},
		},
	}
}

// assertNoUnguardedFixAdvice fails if the raw tip appears anywhere that is not
// inside the gate's own "Original tip was: ..." rewrite.
func assertNoUnguardedFixAdvice(t *testing.T, label, out string) {
	t.Helper()
	if !strings.Contains(out, rawFixTip) {
		return
	}
	guarded := strings.Count(out, "Original tip was: "+rawFixTip)
	total := strings.Count(out, rawFixTip)
	if guarded != total {
		t.Errorf("%s emitted --fix advice the schema gate had ruled unsafe\n"+
			"  %d occurrence(s), only %d guarded\n---output---\n%s",
			label, total, guarded, out)
	}
}

// TestNoEmitterLeaksUnguardedFixAdvice is the regression test for the central
// defect: the sanitizer used to run inside printDiagnostics only, so --json,
// --agent and --output published advice the gate had already ruled unsafe.
//
// It is written as a property over the whole emitter set rather than as one
// example per format, because the failure mode is "somebody added an output
// path and did not know the guard existed."
func TestNoEmitterLeaksUnguardedFixAdvice(t *testing.T) {
	gate := blockedGate()

	result := resultWithFixTip()
	sanitizeFixAdvice(&result, gate)

	tmp := t.TempDir()

	emitters := []struct {
		name string
		emit func(t *testing.T, r doctorResult) string
	}{
		{
			name: "text/printDiagnostics",
			emit: func(t *testing.T, r doctorResult) string {
				return captureStdout(t, func() error {
					printDiagnostics(r, gate)
					return nil
				})
			},
		},
		{
			name: "json/doctorResult",
			emit: func(t *testing.T, r doctorResult) string {
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return string(b)
			},
		},
		{
			name: "agent/buildAgentResult",
			emit: func(t *testing.T, r doctorResult) string {
				b, err := json.Marshal(buildAgentResult(r))
				if err != nil {
					t.Fatalf("marshal agent result: %v", err)
				}
				return string(b)
			},
		},
		{
			name: "export/exportDiagnostics",
			emit: func(t *testing.T, r doctorResult) string {
				p := filepath.Join(tmp, "diag.json")
				if err := exportDiagnostics(r, p); err != nil {
					t.Fatalf("export: %v", err)
				}
				b, err := os.ReadFile(p) // #nosec G304 - test-controlled path
				if err != nil {
					t.Fatalf("read export: %v", err)
				}
				return string(b)
			},
		},
	}

	for _, e := range emitters {
		t.Run(e.name, func(t *testing.T) {
			assertNoUnguardedFixAdvice(t, e.name, e.emit(t, result))
		})
	}
}

// TestSanitizeFixAdviceWritesThroughSlice pins the specific Go mistake that
// made the original guard a no-op: ranging by value over []doctorCheck mutates
// a copy. Asserting on the caller's slice — not on a returned value — is what
// makes this test able to fail.
func TestSanitizeFixAdviceWritesThroughSlice(t *testing.T) {
	result := resultWithFixTip()
	sanitizeFixAdvice(&result, blockedGate())

	if result.Checks[0].Fix == rawFixTip {
		t.Fatal("sanitizeFixAdvice did not write through to result.Checks — " +
			"the mutation was applied to a copy")
	}
	if !strings.Contains(result.Checks[0].Fix, "Do NOT run") {
		t.Fatalf("expected a guarded tip, got %q", result.Checks[0].Fix)
	}
}

// TestSanitizeFixAdviceIsIdempotent guards against double-wrapping now that the
// pass runs again after --fix re-runs diagnostics.
func TestSanitizeFixAdviceIsIdempotent(t *testing.T) {
	gate := blockedGate()
	result := resultWithFixTip()

	sanitizeFixAdvice(&result, gate)
	once := result.Checks[0].Fix
	sanitizeFixAdvice(&result, gate)
	twice := result.Checks[0].Fix

	if strings.Count(twice, "Original tip was:") > 1 {
		t.Fatalf("sanitizing twice nested the guard:\n once: %q\ntwice: %q", once, twice)
	}
}

// TestCheckFlagWrites enumerates which --check= invocations can modify state.
// The gate is applied at a single branch point in RunE keyed off this function,
// so this table is the complete statement of what is guarded — a new
// destructive check that is missing here is a silent bypass.
func TestCheckFlagWrites(t *testing.T) {
	cases := []struct {
		flag  string
		clean bool
		fix   bool
		want  bool
	}{
		{"pollution", true, false, true},
		{"pollution", false, false, false},
		{"artifacts", true, false, true},
		{"artifacts", false, false, false},
		{"validate", false, true, true},
		{"validate", false, false, false},
		{"conventions", true, true, false},
		{"unknown-check", true, true, false},
	}
	for _, c := range cases {
		if got := checkFlagWrites(c.flag, c.clean, c.fix); got != c.want {
			t.Errorf("checkFlagWrites(%q, clean=%v, fix=%v) = %v, want %v",
				c.flag, c.clean, c.fix, got, c.want)
		}
	}
}

// TestCollectFixableIssuesPartitionsByBlastRadius pins the split that lets a
// schema gate withhold schema writes without also refusing to repair a file
// mode — the "no hatch for filesystem-only fixes" complaint.
func TestCollectFixableIssuesPartitionsByBlastRadius(t *testing.T) {
	result := doctorResult{
		Checks: []doctorCheck{
			{Name: "Gitignore", Status: statusWarning, Fix: "add entries"},
			{Name: "Permissions", Status: statusError, Fix: "chmod"},
			{Name: "Schema Compatibility", Status: statusError, Fix: "migrate"},
			{Name: "Database", Status: statusWarning, Fix: "repair"},
			{Name: "Healthy Thing", Status: statusOK, Fix: "should be ignored"},
			{Name: "No Fix Available", Status: statusError},
		},
	}

	dbFixes, fsFixes := collectFixableIssues(result)

	if len(fsFixes) != 2 {
		t.Errorf("expected 2 filesystem fixes, got %d: %+v", len(fsFixes), fsFixes)
	}
	if len(dbFixes) != 2 {
		t.Errorf("expected 2 database fixes, got %d: %+v", len(dbFixes), dbFixes)
	}
	for _, f := range fsFixes {
		if !doctor.IsFilesystemOnlyFix(f.Name) {
			t.Errorf("%q was partitioned as filesystem-only but is not", f.Name)
		}
	}
}
