// Tests for doctor's mutation gate (#6028): `bd doctor --fix` / `--clean` must
// refuse to run under strict --readonly, exactly like the ~120 write commands
// that call CheckReadonly at the top of their RunE. Doctor never made that call
// — it opts out of the root PersistentPreRunE store init via
// skipStoreAnnotation, which also opted it out of that hook's gates — so both
// --readonly and an active MIGRATION-FREEZE were bypassed structurally.
//
// The freeze half of the same gate lives in migration_freeze_gate_test.go,
// beside the rest of the freeze suite; both halves share the helpers below and
// the hermetic subprocess harness declared there.
//
// This file MUST NOT carry a cgo build tag: it drives a bd binary built with
// the gms_pure_go tag via subprocess, and asserts only on files and output.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctorMutationSurface is one class of doctor invocation that mutates the
// workspace. Section 2 of the #6028 design enumerates the complete set: every
// mutating path is reached through --fix or --clean and nothing else, so these
// five rows cover the whole write surface, including fixers added later.
type doctorMutationSurface struct {
	name string
	args []string
	// op is the operation label the gate reports, derived from which flag made
	// the run mutating (--fix wins when both are set).
	op string
}

func doctorMutationSurfaces() []doctorMutationSurface {
	return []doctorMutationSurface{
		{name: "fix", args: []string{"doctor", "--fix", "--yes"}, op: "doctor --fix"},
		{name: "fix-interactive", args: []string{"doctor", "--fix", "-i"}, op: "doctor --fix"},
		{name: "validate-fix", args: []string{"doctor", "--check=validate", "--fix", "--yes"}, op: "doctor --fix"},
		{name: "pollution-clean", args: []string{"doctor", "--check=pollution", "--clean", "--yes"}, op: "doctor --clean"},
		{name: "artifacts-clean", args: []string{"doctor", "--check=artifacts", "--clean", "--yes"}, op: "doctor --clean"},
	}
}

// doctorWorkspaceFingerprint captures the two workspace files a refused doctor
// run is most likely to have touched: .local_version (trackBdVersion's target)
// and metadata.json (the Metadata Config / Database fixers' target).
func doctorWorkspaceFingerprint(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	fingerprint := make(map[string][]byte)
	for _, name := range []string{localVersionFile, "metadata.json"} {
		path := filepath.Join(dir, ".beads", name)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				t.Fatalf("fingerprinting %s: %v", path, err)
			}
			continue
		}
		fingerprint[name] = data
	}
	return fingerprint
}

// assertDoctorWorkspaceUnchanged proves the refusal happened before any
// mutation, not merely that the refusal message was printed.
func assertDoctorWorkspaceUnchanged(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	after := doctorWorkspaceFingerprint(t, dir)
	if len(after) != len(before) {
		t.Errorf("workspace file set changed across a refused doctor run: had %d tracked files, now %d", len(before), len(after))
	}
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf(".beads/%s was deleted by a refused doctor run", name)
			continue
		}
		if string(got) != string(want) {
			t.Errorf(".beads/%s was rewritten by a refused doctor run:\nbefore:\n%s\nafter:\n%s", name, want, got)
		}
	}
}

// assertDoctorRefusalIsClean checks the shared shape of every gate refusal:
// nothing on stdout, and none of the output a fix or clean run would produce.
func assertDoctorRefusalIsClean(t *testing.T, stdout string) {
	t.Helper()
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when doctor is refused, got:\n%s", stdout)
	}
	for _, marker := range []string{"Applying fixes", "Deleting", "Verifying fixes"} {
		if strings.Contains(stdout, marker) {
			t.Errorf("refused doctor run produced %q on stdout — it started mutating before refusing:\n%s", marker, stdout)
		}
	}
}

// TestDoctorMutationBlockedInReadonlyMode is the readonly half of the gate.
// One workspace serves every row: a refused run must leave it untouched, so a
// row that leaks a mutation is caught by the next row's fingerprint too.
func TestDoctorMutationBlockedInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	before := doctorWorkspaceFingerprint(t, dir)

	for _, tt := range doctorMutationSurfaces() {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.args...), "--readonly")
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)

			if code != 1 {
				t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			want := "operation '" + tt.op + "' is not allowed in read-only mode"
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
			assertDoctorRefusalIsClean(t, stdout)
			assertDoctorWorkspaceUnchanged(t, dir, before)
		})
	}
}

// TestDoctorDiagnosisWorksInReadonlyMode pins the other half of the contract:
// the gate keys on --fix/--clean alone, so every diagnosis mode still runs
// under --readonly. Diagnosing a sandboxed workspace is the whole point.
func TestDoctorDiagnosisWorksInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	for _, args := range [][]string{
		{"doctor"},
		{"doctor", "--dry-run"},
		{"doctor", "--check=pollution"},
		{"doctor", "--check=artifacts"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr, _ := runBDMigrationFreeze(t, bd, dir, append(args, "--readonly")...)
			if strings.Contains(stderr, "not allowed in read-only mode") {
				t.Errorf("bd %v was refused under --readonly but mutates nothing:\nstderr:\n%s\nstdout:\n%s", args, stderr, stdout)
			}
		})
	}
}

// TestDoctorMaintenanceSkippedInReadonlyMode is the readonly twin of
// TestDoctorMaintenanceSkippedDuringMigrationFreeze — see that test for why
// plain `bd doctor` had hidden writes at all, and for the shared-server setup.
func TestDoctorMaintenanceSkippedInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	seedStaleLocalVersion(t, dir)

	stdout, stderr, _ := runBDMigrationFreezeWithEnv(t, bd, dir, doctorMaintenanceEnv(), "doctor", "--readonly")

	assertDoctorReportedStaleVersionWithoutHealing(t, dir, stdout, stderr)
	if strings.Contains(stderr, "not allowed in read-only mode") {
		t.Errorf("plain 'bd doctor --readonly' must diagnose, not refuse:\nstderr:\n%s", stderr)
	}
}

// TestDoctorMutationNotBlockedWithoutGates is the regression-safety control:
// with no freeze sentinel and no --readonly, the gate must be invisible. The
// fix outcome itself is not this test's business — only that neither refusal
// fires and neither exit path is the gate's.
func TestDoctorMutationNotBlockedWithoutGates(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	for _, tt := range doctorMutationSurfaces() {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, tt.args...)
			if code == ExitMigrationFrozen || strings.Contains(stderr, "frozen for migration") {
				t.Errorf("bd %v hit the freeze gate with no marker present (exit %d):\nstderr:\n%s", tt.args, code, stderr)
			}
			if strings.Contains(stderr, "not allowed in read-only mode") {
				t.Errorf("bd %v hit the readonly gate without --readonly (exit %d):\nstderr:\n%s", tt.args, code, stderr)
			}
			_ = stdout
		})
	}
}

// TestDoctorMaintenanceStillRunsWithoutGates is the companion control for the
// diagnosis-path change: unfrozen and not read-only, plain `bd doctor` must
// still reconcile a stale .local_version exactly as it did before #6028.
func TestDoctorMaintenanceStillRunsWithoutGates(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	seedStaleLocalVersion(t, dir)

	stdout, stderr, _ := runBDMigrationFreezeWithEnv(t, bd, dir, doctorMaintenanceEnv(), "doctor")

	if !strings.Contains(stderr, "auto-migrate:") {
		t.Errorf("expected autoMigrateOnVersionBump to run (an 'auto-migrate:' debug line) with no gate active, got none:\nstderr:\n%s", stderr)
	}
	got := readWorkspaceLocalVersion(t, dir)
	if got == staleLocalVersion {
		t.Errorf("%s = %q — trackBdVersion did not run with no gate active; the #6028 skip is too broad:\nstdout:\n%s",
			localVersionFile, got, stdout)
	}
}

// TestDoctorReadonlyWinsOverMigrationFreeze pins the branch order inside the
// gate: --readonly is checked first, so a workspace that is both frozen and
// sandboxed reports the readonly refusal. This mirrors CheckReadonly, which
// also answers readonly before delegating to the freeze check. Either refusal
// stops the run, but they carry different exit codes — 1 vs ExitMigrationFrozen
// — so which one wins is observable, and the precedence has to be deliberate.
func TestDoctorReadonlyWinsOverMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	writeFreezeMarker(t, dir, "migrator", "dolt v2 migration")
	before := doctorWorkspaceFingerprint(t, dir)

	stdout, stderr, code := runBDFrozen(t, bd, dir, "doctor", "--fix", "--yes", "--readonly")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (not %d — readonly is answered before the freeze)\nstdout:\n%s\nstderr:\n%s",
			code, ExitMigrationFrozen, stdout, stderr)
	}
	if !strings.Contains(stderr, "operation 'doctor --fix' is not allowed in read-only mode") {
		t.Errorf("stderr missing the readonly refusal (readonly must be checked before the freeze):\n%s", stderr)
	}
	if strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr carries the freeze refusal too; the gate should report exactly one reason:\n%s", stderr)
	}
	assertDoctorRefusalIsClean(t, stdout)
	assertDoctorWorkspaceUnchanged(t, dir, before)
}
