package main

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// gateStub builds the accessor destructiveCheckRefusal takes, counting how many
// times the gate was actually evaluated.
func gateStub(g doctor.FixGate, calls *atomic.Int32) func() doctor.FixGate {
	return func() doctor.FixGate {
		calls.Add(1)
		return g
	}
}

// presentButUnopenableGate is the shape AssessSchemaFixGate returns for a
// database that is on disk but could not be opened.
func presentButUnopenableGate() doctor.FixGate {
	return doctor.FixGate{
		DBPresent:  true,
		AllowFSFix: true,
		Reason:     "database exists but could not be opened, so its schema version is unknown",
	}
}

// TestDestructiveCheckRefusalRefusesOnPresentButUnopenableDatabase is the
// call-site half of the PR #5145 fail-open: the refusal must fire for a database
// that exists but could not be opened, because the handler it guards opens the
// store through the migrating factory, which auto-starts a stopped server and
// applies pending migrations before writing.
func TestDestructiveCheckRefusalRefusesOnPresentButUnopenableDatabase(t *testing.T) {
	var calls atomic.Int32
	var err error
	// HandleErrorWithHint reports on stderr and returns a bare exit code, so the
	// refusal text — which interpolates the gate's Reason — is only observable
	// there.
	stderr := captureStderrDuring(t, func() {
		err = destructiveCheckRefusal("pollution", true, false, gateStub(presentButUnopenableGate(), &calls))
	})

	if err == nil {
		t.Fatal("destructiveCheckRefusal returned nil; --check=pollution --clean must be refused on an unopenable database")
	}
	if !strings.Contains(stderr, "refusing destructive 'bd doctor --check=pollution'") {
		t.Errorf("refusal did not name the command it refused:\n%s", stderr)
	}
	if !strings.Contains(stderr, "could not be opened") {
		t.Errorf("refusal did not explain why, so the operator cannot act on it:\n%s", stderr)
	}
	if calls.Load() != 1 {
		t.Errorf("gate evaluated %d times, want exactly 1", calls.Load())
	}
}

// TestDestructiveCheckRefusalAllowsFilesystemOnlyCleanup pins the two planes
// agreeing about one operation: `bd doctor --fix` admits "Classic Artifacts"
// under a blocked gate because it is filesystem-only, so
// `--check=artifacts --clean` must not be refused with a schema-skew message in
// exactly the degraded state where clearing stale artifacts is safe and useful.
func TestDestructiveCheckRefusalAllowsFilesystemOnlyCleanup(t *testing.T) {
	blocked := []struct {
		name string
		gate doctor.FixGate
	}{
		{"present but unopenable", presentButUnopenableGate()},
		{"pending", doctor.FixGate{Determined: true, DBReachable: true, DBPresent: true, Pending: true, AllowFSFix: true, Reason: "pending"}},
		{"ahead", doctor.FixGate{Determined: true, DBReachable: true, DBPresent: true, Ahead: true, AllowFSFix: true, Reason: "ahead"}},
	}
	for _, b := range blocked {
		var calls atomic.Int32
		if err := destructiveCheckRefusal("artifacts", true, false, gateStub(b.gate, &calls)); err != nil {
			t.Errorf("%s gate: --check=artifacts --clean refused, but the same cleanup is admitted by --fix: %v", b.name, err)
		}
		// The same gate must still refuse the row-deleting sibling, or this test
		// would pass by disabling the guard rather than by scoping it.
		if err := destructiveCheckRefusal("pollution", true, false, gateStub(b.gate, &calls)); err == nil {
			t.Errorf("%s gate: --check=pollution --clean was admitted; it deletes rows and must stay refused", b.name)
		}
		if err := destructiveCheckRefusal("validate", false, true, gateStub(b.gate, &calls)); err == nil {
			t.Errorf("%s gate: --check=validate --fix was admitted; it writes through an opened store", b.name)
		}
	}
}

// TestDestructiveCheckRefusalAdmitsWhenNoDatabaseExists keeps the no-hazard case
// unguarded: with no database on disk there is no schema to skew, so refusing
// would block a cleanup that cannot possibly migrate anything.
func TestDestructiveCheckRefusalAdmitsWhenNoDatabaseExists(t *testing.T) {
	noDB := doctor.FixGate{Determined: true, RecommendFix: true, AllowFSFix: true}
	for _, flag := range []string{"artifacts", "pollution"} {
		var calls atomic.Int32
		if err := destructiveCheckRefusal(flag, true, false, gateStub(noDB, &calls)); err != nil {
			t.Errorf("--check=%s --clean refused with no database present: %v", flag, err)
		}
	}
}

// TestDestructiveCheckRefusalDoesNotProbeReadOnlyChecks pins the laziness the
// gate's contract claims: a read-only `--check` must not evaluate the gate at
// all, because the probe is the only thing that would touch the database on a
// path that otherwise does not.
func TestDestructiveCheckRefusalDoesNotProbeReadOnlyChecks(t *testing.T) {
	cases := []struct {
		flag  string
		clean bool
		fix   bool
	}{
		{"artifacts", false, false},
		{"pollution", false, false},
		{"conventions", true, true},
		{"validate", true, false},
	}
	for _, c := range cases {
		var calls atomic.Int32
		if err := destructiveCheckRefusal(c.flag, c.clean, c.fix, gateStub(presentButUnopenableGate(), &calls)); err != nil {
			t.Errorf("--check=%s (clean=%v fix=%v) refused although it cannot write: %v", c.flag, c.clean, c.fix, err)
		}
		if calls.Load() != 0 {
			t.Errorf("--check=%s (clean=%v fix=%v) evaluated the gate %d times; a read-only check must not probe",
				c.flag, c.clean, c.fix, calls.Load())
		}
	}
}

// TestNewSchemaGateIsLazyAndMemoised pins the half of the gate's stated contract
// that is a real invariant rather than a call-order claim: one verdict per
// invocation, produced on first use and reused by every later consumer. The
// assessor is the only thing in the command that opens a connection for the
// gate, so evaluating it eagerly would probe on read-only paths, and evaluating
// it twice would let two consumers in one invocation disagree.
func TestNewSchemaGateIsLazyAndMemoised(t *testing.T) {
	var calls atomic.Int32
	original := assessSchemaFixGate
	t.Cleanup(func() { assessSchemaFixGate = original })
	assessSchemaFixGate = func(path string) doctor.FixGate {
		calls.Add(1)
		return doctor.FixGate{Determined: true, DBReachable: true, DBPresent: true, DBVersion: 7, AllowFSFix: true}
	}

	gate := newSchemaGate("/some/repo")
	if calls.Load() != 0 {
		t.Fatalf("assessor ran %d times before first use; the gate must be lazy", calls.Load())
	}

	first := gate()
	for i := 0; i < 4; i++ {
		if got := gate(); got != first {
			t.Fatalf("gate() returned %+v on call %d, want the memoised %+v", got, i+2, first)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("assessor ran %d times across five reads, want exactly 1", calls.Load())
	}
}

// TestNewSchemaGatePassesTheRepoPathThrough guards against a seam that reads a
// different workspace than the command was pointed at.
func TestNewSchemaGatePassesTheRepoPathThrough(t *testing.T) {
	original := assessSchemaFixGate
	t.Cleanup(func() { assessSchemaFixGate = original })
	var seen string
	assessSchemaFixGate = func(path string) doctor.FixGate {
		seen = path
		return doctor.FixGate{}
	}

	newSchemaGate("/repo/under/test")()

	if seen != "/repo/under/test" {
		t.Errorf("assessor received %q, want %q", seen, "/repo/under/test")
	}
}

// TestApplyFixesReportsWithheldFilesystemOnlyFix pins the reporting hole: a
// filesystem-only fix the gate rejects used to be appended to neither the
// fixable list nor the withheld list, so it was silently no-opped with no
// output. Unreachable while every assessed gate sets AllowFSFix, which is why
// this drives the gate shape directly.
func TestApplyFixesReportsWithheldFilesystemOnlyFix(t *testing.T) {
	result := doctorResult{
		Checks: []doctorCheck{
			{Name: "Gitignore", Status: statusWarning, Fix: "add entries"},
		},
	}
	// AllowFSFix false is the shape that exposed the hole.
	out := captureStdout(t, func() error {
		applyFixes(result, doctor.FixGate{DBReachable: true, DBPresent: true, Reason: "schema state unknown"})
		return nil
	})

	if !strings.Contains(out, "Gitignore") {
		t.Errorf("withheld filesystem-only fix was not reported at all:\n%s", out)
	}
	if !strings.Contains(out, "Skipping 1 gated fix(es)") {
		t.Errorf("withheld count/label missing; the withheld set is whatever the gate blocked:\n%s", out)
	}
	if strings.Contains(out, "database fix(es)") {
		t.Errorf("a withheld filesystem-only fix was labelled a database fix:\n%s", out)
	}
}
