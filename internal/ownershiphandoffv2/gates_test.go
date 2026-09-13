package ownershiphandoffv2

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
)

// stubPortHolder replaces the port-holder lookup for one test.
func stubPortHolder(t *testing.T, fn func(port int, dir string) (int, string, doltserver.PortHolderOutcome)) {
	t.Helper()
	prior := resolvePortHolder
	resolvePortHolder = fn
	t.Cleanup(func() { resolvePortHolder = prior })
}

// stubProcid replaces the birth-identity primitives for one test, modelling a
// platform where they are not implemented.
func stubProcid(t *testing.T, capture func(int) (procid.Token, error), verify func(int, procid.Token) (bool, error)) {
	t.Helper()
	priorCapture, priorVerify := captureBirth, verifyBirth
	captureBirth, verifyBirth = capture, verify
	t.Cleanup(func() { captureBirth, verifyBirth = priorCapture, priorVerify })
}

var errNoBirthIdentity = errors.New("procid: process-birth identity is not implemented on this platform")

// An undetermined port-holder lookup is not a free port. The merged
// findPIDOnPort collapses "nothing is listening" and "lsof failed" into 0, and
// a gate that reads that as proof of absence fails open — which is exactly
// backwards for a gate whose job is to refuse.
func TestUndeterminedPortHolderNeverPassesTheReleaseGate(t *testing.T) {
	stubPortHolder(t, func(int, string) (int, string, doltserver.PortHolderOutcome) {
		return 0, "", doltserver.PortHolderUndetermined
	})
	x := &run{j: Journal{Phase: PhasePrepared}, req: Request{Endpoint: Endpoint{Host: "127.0.0.1", Port: 1}}}
	e := newEvidence("test")

	pid, _, outcome := resolvePortHolder(x.req.Endpoint.Port, "")
	switch outcome {
	case doltserver.PortHolderHeld:
		t.Fatalf("stub produced held for pid %d", pid)
	case doltserver.PortHolderNoHolder:
		e.record("port_released", GatePassed)
	default:
		e.record("port_released", GateUnavailable)
	}
	if got := e.Gates["port_released"]; got != GateUnavailable {
		t.Fatalf("undetermined lookup recorded %q, want %q", got, GateUnavailable)
	}
	if !e.hasUnavailable() {
		t.Fatal("evidence does not report itself incomplete")
	}
}

// The same rule for §3: a lookup that did not run produces an unresolved
// instance with a reason distinct from "the lookup ran and found nothing", so
// an operator can tell a gate bd could not evaluate from one it evaluated.
func TestUndeterminedPortHolderYieldsDistinctUnresolvedReason(t *testing.T) {
	if doltserver.PortHolderSource() == "" {
		t.Skip("no port-holder lookup on this platform; the reason is already undetermined")
	}
	stubPortHolder(t, func(int, string) (int, string, doltserver.PortHolderOutcome) {
		return 0, "", doltserver.PortHolderUndetermined
	})
	undetermined := captureInstance(Endpoint{Host: "127.0.0.1", Port: 1}, t.TempDir())
	if undetermined.Resolved {
		t.Fatal("an undetermined lookup produced a resolved instance")
	}
	if undetermined.Reason != reasonPortHolderUndetermined {
		t.Fatalf("reason is %q, want %q", undetermined.Reason, reasonPortHolderUndetermined)
	}

	stubPortHolder(t, func(int, string) (int, string, doltserver.PortHolderOutcome) {
		return 0, "", doltserver.PortHolderNoHolder
	})
	absent := captureInstance(Endpoint{Host: "127.0.0.1", Port: 1}, t.TempDir())
	if absent.Resolved {
		t.Fatal("a no-holder lookup produced a resolved instance")
	}
	if absent.Reason == reasonPortHolderUndetermined {
		t.Fatal("a lookup that ran and found nothing is recorded as undetermined")
	}
}

// Where process-birth identity is unimplemented, the disproof gate is
// unavailable: never passed (bd proved nothing) and never a refusal (bd
// observed nothing that says the server is alive).
func TestUnsupportedBirthIdentityMakesTheDisproofGateUnavailable(t *testing.T) {
	stubProcid(t,
		func(int) (procid.Token, error) { return "", errNoBirthIdentity },
		func(int, procid.Token) (bool, error) { return false, errNoBirthIdentity })

	resolved := Instance{Resolved: true, PID: 4242, Birth: "captured-at-prepare"}
	gone, ok, detail := instanceGone(resolved)
	if ok {
		t.Fatalf("gate claims to have answered on a platform that cannot: gone=%v detail=%q", gone, detail)
	}
	if gone {
		t.Fatal("gate reported the process gone without being able to look")
	}

	// And the capture side: an unresolved instance, not a crash.
	if doltserver.PortHolderSource() != "" {
		stubPortHolder(t, func(int, string) (int, string, doltserver.PortHolderOutcome) {
			return 4242, "fd-lock", doltserver.PortHolderHeld
		})
		inst := captureInstance(Endpoint{Host: "127.0.0.1", Port: 1}, t.TempDir())
		if inst.Resolved {
			t.Fatal("captured a birth identity on a platform that has none")
		}
		if inst.PID != 4242 || inst.BoundBy != "fd-lock" {
			t.Fatalf("unresolved instance lost what bd did observe: %+v", inst)
		}
	}
}

// An instance that was never resolved cannot be disproved either: (b) is
// unavailable, which is what licenses a commit whose evidence is incomplete
// rather than blocking the transfer outright.
func TestUnresolvedInstanceMakesTheDisproofGateUnavailable(t *testing.T) {
	gone, ok, _ := instanceGone(Instance{Resolved: false, Reason: reasonPortHolderUndetermined})
	if ok || gone {
		t.Fatalf("unresolved instance was treated as answerable (gone=%v ok=%v)", gone, ok)
	}
}

func TestDataDirLockedDetectsAHeldNomsLock(t *testing.T) {
	dir := t.TempDir()
	nomsDir := filepath.Join(dir, "scope_db", ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockPath := filepath.Join(nomsDir, "LOCK")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("write LOCK: %v", err)
	}

	locked, ok, detail := dataDirLocked(dir)
	if !ok {
		t.Fatalf("probe could not answer: %s", detail)
	}
	if locked {
		t.Fatalf("an unheld LOCK was reported held: %s", detail)
	}

	held := holdLock(t, lockPath)
	defer held()
	locked, ok, detail = dataDirLocked(dir)
	if !ok {
		t.Fatalf("probe could not answer while held: %s", detail)
	}
	if !locked {
		t.Fatalf("a held LOCK was reported free: %s", detail)
	}
}

// The fence admits nothing while a transfer is in flight, and admits normally
// once it has settled in either direction.
func TestFenceAdmitsOnlySettledJournals(t *testing.T) {
	for _, phase := range everyPhase {
		t.Run(string(phase), func(t *testing.T) {
			root := t.TempDir()
			beadsDir := BeadsDir(root)
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			j := journalAt(t, root, phase)
			if err := save(filepath.Join(beadsDir, JournalName), j); err != nil {
				t.Fatalf("save: %v", err)
			}

			err := CheckNormalOpen(beadsDir)
			switch phase {
			case PhaseCommitted:
				if err != nil {
					t.Fatalf("fence refused a committed journal: %v", err)
				}
			case PhaseRolledBack:
				// Admitted because the snapshot is empty and therefore trivially
				// restored, and archived as a side effect so the fence has an exit.
				if err != nil {
					t.Fatalf("fence refused a settled rollback: %v", err)
				}
				if _, statErr := os.Lstat(filepath.Join(beadsDir, JournalName)); !os.IsNotExist(statErr) {
					t.Fatal("admitting a rolled-back journal did not archive it")
				}
			default:
				if err == nil {
					t.Fatalf("fence admitted an in-flight handoff at %s", phase)
				}
				if !IsFenced(err) {
					t.Fatalf("refusal at %s is not reported as a fence: %v", phase, err)
				}
			}
		})
	}
}

func TestFenceAdmitsAWorkspaceWithNoJournal(t *testing.T) {
	beadsDir := BeadsDir(t.TempDir())
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := CheckNormalOpen(beadsDir); err != nil {
		t.Fatalf("fence refused a workspace with no handoff: %v", err)
	}
}

// A v1 journal reaching the fence is refused with the version code, not
// silently admitted and not reported as generic corruption.
func TestFenceRefusesAV1Journal(t *testing.T) {
	root := t.TempDir()
	beadsDir := BeadsDir(root)
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	v1 := `{"phase": "committed", "owner": "bd", "request": {"root": "` + root + `"}}`
	if err := os.WriteFile(filepath.Join(beadsDir, JournalName), []byte(v1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := CheckNormalOpen(beadsDir)
	if ErrorCode(err) != CodeUnsupportedJournalVersion {
		t.Fatalf("fence returned %q, want %q (error: %v)", ErrorCode(err), CodeUnsupportedJournalVersion, err)
	}
}

func TestArtifactCaptureRestoreIsByteAndModeExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("dolt:\n  port: 3307\n"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	captured, err := captureArtifact(path)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	if err := os.WriteFile(path, []byte("dolt:\n  port: 9999\n# rewritten\n"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if err := artifactMatches(path, captured); err == nil {
		t.Fatal("a rewritten file compared equal to its snapshot")
	}
	if err := restoreArtifact(path, captured); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := artifactMatches(path, captured); err != nil {
		t.Fatalf("restore was not exact: %v", err)
	}
}

// Present=false means the file was not there, and restoring it means removing
// whatever is there now. Getting this wrong leaves bd's own lifecycle files
// behind in a workspace that never had any.
func TestRestoringAnAbsentArtifactRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dolt-server.pid")
	captured, err := captureArtifact(path)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if captured.Present {
		t.Fatal("captured a file that does not exist as present")
	}
	if err := os.WriteFile(path, []byte("4242"), 0o600); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := restoreArtifact(path, captured); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("restoring an absent artifact left the file behind")
	}
}

func TestBuildRequestRefusesNonLoopbackAndMalformedInput(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		opts Options
		code string
	}{
		{"no root", Options{Database: "d", Workspace: "w", Endpoint: Endpoint{Host: "127.0.0.1", Port: 1}}, CodeInvalidRequest},
		{"relative root", Options{Root: "rel", Database: "d", Workspace: "w", Endpoint: Endpoint{Host: "127.0.0.1", Port: 1}}, CodeInvalidRequest},
		{"no database", Options{Root: root, Workspace: "w", Endpoint: Endpoint{Host: "127.0.0.1", Port: 1}}, CodeInvalidRequest},
		{"no workspace", Options{Root: root, Database: "d", Endpoint: Endpoint{Host: "127.0.0.1", Port: 1}}, CodeInvalidRequest},
		{"no port", Options{Root: root, Database: "d", Workspace: "w", Endpoint: Endpoint{Host: "127.0.0.1"}}, CodeInvalidRequest},
		{"remote host", Options{Root: root, Database: "d", Workspace: "w", Endpoint: Endpoint{Host: "10.0.0.5", Port: 1}}, CodeUnsupportedScope},
		{"hostname not resolved", Options{Root: root, Database: "d", Workspace: "w", Endpoint: Endpoint{Host: "localhost", Port: 1}}, CodeUnsupportedScope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRequest(tc.opts); ErrorCode(err) != tc.code {
				t.Fatalf("got %q, want %q (error: %v)", ErrorCode(err), tc.code, err)
			}
		})
	}
}

// A journal belongs to exactly one scope. A verb naming a different one is
// refused rather than merged into it, because the journal carries a snapshot
// and reservations for a scope the new request was never observed against.
func TestConflictingRequestIsRefusedNotMerged(t *testing.T) {
	have := Request{
		Root: "/srv/scope", Database: "scope_db", Workspace: "w",
		Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307},
	}
	for _, want := range []Request{
		{Root: "/srv/other", Database: "scope_db", Workspace: "w", Endpoint: have.Endpoint},
		{Root: have.Root, Database: "other_db", Workspace: "w", Endpoint: have.Endpoint},
		{Root: have.Root, Database: "scope_db", Workspace: "other", Endpoint: have.Endpoint},
		{Root: have.Root, Database: "scope_db", Workspace: "w", Endpoint: Endpoint{Host: "127.0.0.1", Port: 9999}},
	} {
		if err := sameScope(have, want); ErrorCode(err) != CodeIdentityConflict {
			t.Fatalf("sameScope(%+v) returned %q, want %q", want, ErrorCode(err), CodeIdentityConflict)
		}
	}
	if err := sameScope(have, have); err != nil {
		t.Fatalf("sameScope refused an identical request: %v", err)
	}
}

func TestSentinelsDistinguishMissingFromEmpty(t *testing.T) {
	missing := Sentinels{IssuesState: TableMissing, DependenciesState: TableMissing, HeadHash: "abc"}
	empty := Sentinels{IssuesState: TableEmpty, DependenciesState: TableEmpty, HeadHash: "abc"}
	if _, equal := sentinelsEqual(missing, empty); equal {
		t.Fatal("a database with no schema compared equal to an empty one")
	}
	if _, equal := sentinelsEqual(empty, empty); !equal {
		t.Fatal("two identical captures compared unequal")
	}
	different := empty
	different.HeadHash = "def"
	if _, equal := sentinelsEqual(empty, different); equal {
		t.Fatal("two different commits compared equal")
	}
}
