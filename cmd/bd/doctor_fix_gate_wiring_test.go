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
				b, err := json.Marshal(buildAgentResult(r, gate))
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

// assertAgentCommandsGuarded fails if any remediation command in the agent
// output steers at `bd doctor --fix` without the gate's rewrite marker.
// Agent enrichers hardcode their own commands, so the raw tip used by the
// other emitters is not what leaks here; the command text itself is.
func assertAgentCommandsGuarded(t *testing.T, label string, ar agentDoctorResult) {
	t.Helper()
	for _, d := range ar.Diagnostics {
		for _, c := range d.Commands {
			if doctor.MentionsFixAdvice(c) && !strings.Contains(c, "Original tip was: ") {
				t.Errorf("%s: check %q (%s) published unguarded --fix command %q under a blocked gate",
					label, d.Name, d.Status, c)
			}
		}
	}
}

// TestNoAgentEnricherLeaksUnguardedFixCommand runs the --agent emitter over
// EVERY registered enricher, plus a name with no enricher (generic path).
// The original fixture used only an unenriched name, so the ~18 enrichers that
// hardcode `bd doctor --fix` were never exercised and the test passed
// vacuously. Both statuses are covered because some enrichers branch on it.
func TestNoAgentEnricherLeaksUnguardedFixCommand(t *testing.T) {
	names := append(doctor.AgentEnricherNames(), "Tracked Runtime Files")
	if len(names) < 20 {
		t.Fatalf("expected the full enricher registry plus the unenriched case, got %d names", len(names))
	}

	build := func(gate doctor.FixGate) agentDoctorResult {
		var r doctorResult
		for _, name := range names {
			for _, status := range []string{statusError, statusWarning} {
				r.Checks = append(r.Checks, doctorCheck{
					Name: name, Status: status, Message: "m", Category: "Core", Fix: rawFixTip,
				})
			}
		}
		sanitizeFixAdvice(&r, gate)
		return buildAgentResult(r, gate)
	}

	// Non-vacuity: under a safe gate the fixture must actually surface
	// `bd doctor --fix` commands from enrichers, or the blocked-gate assertion
	// below proves nothing. Schema Compatibility and Database Integrity are
	// the checks that fire on the very skew this gate exists for.
	surfaced := map[string]bool{}
	for _, d := range build(doctor.FixGate{Determined: true, DBReachable: true, RecommendFix: true, AllowDBFix: true, AllowFSFix: true}).Diagnostics {
		for _, c := range d.Commands {
			if doctor.MentionsFixAdvice(c) {
				surfaced[d.Name] = true
			}
		}
	}
	for _, must := range []string{"Schema Compatibility", "Database Integrity", "Tracked Runtime Files"} {
		if !surfaced[must] {
			t.Fatalf("fixture too weak: %q surfaced no --fix command under a safe gate", must)
		}
	}
	if len(surfaced) < 15 {
		t.Fatalf("fixture too weak: only %d names surfaced --fix commands", len(surfaced))
	}

	assertAgentCommandsGuarded(t, "blocked gate", build(blockedGate()))
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

// unreachableGate mirrors the `unreachable` FixGate literal in
// cmd/bd/doctor/fix_gate.go (AssessSchemaFixGate, openDoltDB failure branch):
// Determined and AllowFSFix are true, but AllowDBFix and Reason are left at
// their zero values (false, ""). A gate keyed off `gate.Reason == ""` reads
// this shape as "safe to fix" and admits database fixes on a stopped or
// unreachable Dolt server — the bypass GH#4993 exists to close.
func unreachableGate() doctor.FixGate {
	return doctor.FixGate{
		Determined:    true,
		RecommendFix:  true,
		AllowFSFix:    true,
		BinaryVersion: 49,
	}
}

func resultWithDBFix() doctorResult {
	return doctorResult{
		Path:       "/tmp/does-not-need-to-exist",
		CLIVersion: "test",
		Checks: []doctorCheck{
			{
				Name:     "Schema Compatibility",
				Status:   statusError,
				Message:  "schema version mismatch",
				Category: "Core",
				Fix:      "run 'bd doctor --fix' to migrate",
			},
		},
	}
}

// TestApplyFixesWithholdsDBFixOnUnreachableGate is the regression test for the
// gate bypass: applyFixes admitted a database fix whenever
// `gate.AllowDBFix || gate.Reason == ""`, which is true for the unreachable
// shape above even though AllowDBFix is false. Gating on AllowDBFix alone
// withholds it.
func TestApplyFixesWithholdsDBFixOnUnreachableGate(t *testing.T) {
	gate := unreachableGate()
	result := resultWithDBFix()

	out := captureStdout(t, func() error {
		applyFixes(result, gate)
		return nil
	})

	if strings.Contains(out, "Fixing Schema Compatibility") {
		t.Fatalf("applyFixes attempted a database fix on an unreachable gate:\n%s", out)
	}
	if !strings.Contains(out, "No fixable issues found") {
		t.Fatalf("expected applyFixes to withhold the only fix (a database fix) on an unreachable gate, got:\n%s", out)
	}
}

// withNullStdin points os.Stdin at /dev/null so applyFixes takes its
// non-interactive early return after listing what it would fix, instead of
// prompting or running a real fixer against the test's fake path.
func withNullStdin(t *testing.T) {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = orig
		_ = f.Close()
	})
}

func resultWithRecoveryAndSchemaFixes() doctorResult {
	return doctorResult{
		Path:       "/tmp/does-not-need-to-exist",
		CLIVersion: "test",
		Checks: []doctorCheck{
			{Name: "Corrupt Manifest", Status: statusError, Message: "manifest corrupt", Fix: "bd doctor --fix"},
			{Name: "Dolt Format", Status: statusWarning, Message: "pre-v56 dolt dir", Fix: "bd doctor --fix"},
			{Name: "Database Integrity", Status: statusError, Message: "integrity check failed", Fix: "bd doctor --fix"},
			{Name: "Dolt Schema", Status: statusWarning, Message: "dolt_database missing", Fix: "bd doctor --fix"},
			{Name: "Schema Compatibility", Status: statusError, Message: "schema mismatch", Fix: "bd doctor --fix"},
			{Name: "Pending Migrations", Status: statusWarning, Message: "pending", Fix: "bd doctor --fix"},
		},
	}
}

// TestApplyFixesKeepsRecoveryFixesOnUnreachableGate pins the narrow rule that
// an unopenable database must not withhold the fixes that repair an unopenable
// database: recovery fixers are listed as runnable, schema-writing fixes are
// still skipped.
func TestApplyFixesKeepsRecoveryFixesOnUnreachableGate(t *testing.T) {
	withNullStdin(t)
	out := captureStdout(t, func() error {
		applyFixes(resultWithRecoveryAndSchemaFixes(), unreachableGate())
		return nil
	})

	for _, name := range []string{"Corrupt Manifest", "Dolt Format", "Database Integrity", "Dolt Schema"} {
		if !strings.Contains(out, name+": ") {
			t.Errorf("recovery fix %q was not offered on an unreachable gate:\n%s", name, out)
		}
	}
	for _, name := range []string{"Schema Compatibility", "Pending Migrations"} {
		if strings.Contains(out, name+": ") {
			t.Errorf("schema-writing fix %q was offered on an unreachable gate:\n%s", name, out)
		}
		if !strings.Contains(out, "· "+name) {
			t.Errorf("schema-writing fix %q was not reported as skipped:\n%s", name, out)
		}
	}
}

// TestApplyFixesWithholdsRecoveryFixesWhenDatabaseReachableButBlocked is the
// other edge of the same rule: the exception exists because an unreachable
// database has no readable schema to skew. A reachable database that is ahead
// of the binary still gets backup+reinit withheld.
func TestApplyFixesWithholdsRecoveryFixesWhenDatabaseReachableButBlocked(t *testing.T) {
	withNullStdin(t)
	out := captureStdout(t, func() error {
		applyFixes(resultWithRecoveryAndSchemaFixes(), blockedGate())
		return nil
	})

	if !strings.Contains(out, "No fixable issues found") {
		t.Fatalf("expected every fix withheld on a reachable, skewed gate, got:\n%s", out)
	}
	if strings.Contains(out, "Corrupt Manifest: ") {
		t.Fatalf("recovery fix offered while the database is reachable and ahead:\n%s", out)
	}
}

// TestPreviewFixesLabelsOnlySchemaWritingFixesBlockedOnUnreachableGate is the
// dry-run half of the recovery rule.
func TestPreviewFixesLabelsOnlySchemaWritingFixesBlockedOnUnreachableGate(t *testing.T) {
	out := captureStdout(t, func() error {
		previewFixes(resultWithRecoveryAndSchemaFixes(), unreachableGate())
		return nil
	})
	if got := strings.Count(out, "Blocked by the schema gate"); got != 2 {
		t.Fatalf("expected exactly the 2 schema-writing fixes labelled blocked, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "Would apply 4 fix(es); 2 fix(es) are blocked by the schema gate") {
		t.Fatalf("unexpected dry-run summary:\n%s", out)
	}
}

// TestPreviewFixesFlagsDBFixAsBlockedOnUnreachableGate is the dry-run half of
// the same regression: blockDB used `!gate.AllowDBFix && gate.Reason != ""`,
// which is false (not blocked) for the same unreachable shape, so a dry run
// never warned that the fix would actually be withheld.
func TestPreviewFixesFlagsDBFixAsBlockedOnUnreachableGate(t *testing.T) {
	gate := unreachableGate()
	result := resultWithDBFix()

	out := captureStdout(t, func() error {
		previewFixes(result, gate)
		return nil
	})

	if !strings.Contains(out, "Blocked by the schema gate") {
		t.Fatalf("previewFixes did not flag the database fix as blocked on an unreachable gate:\n%s", out)
	}
	if !strings.Contains(out, "blocked by the schema gate") {
		t.Fatalf("expected the dry-run summary to report the fix(es) as blocked, got:\n%s", out)
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
