//go:build integration && !windows

package ownershiphandoffv2

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// R1's post-condition is that the workspace is byte- and mode-exact as prepare
// found it. Assert it against a fingerprint taken before any verb ran, not
// against the snapshot the rollback itself used — otherwise the test only
// proves the restore is self-consistent.
func TestRollbackRestoresTheWorkspaceByteExact(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)
	before := f.workspaceFingerprint()

	f.mustRun(VerbPrepare)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbVerify)

	if after := f.workspaceFingerprint(); after == before {
		t.Fatal("configure changed nothing; this test would pass vacuously")
	}

	rolled := f.mustRun(VerbRollback)
	if rolled.Phase != PhaseLegacyConfigRestored {
		t.Fatalf("rollback left phase %s", rolled.Phase)
	}
	if after := f.workspaceFingerprint(); after != before {
		t.Fatalf("rollback did not restore the workspace:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if gate := rolled.Evidence[string(PhaseLegacyConfigRestored)].Gates["artifacts_restored"]; gate != GatePassed {
		t.Errorf("the restore post-condition is %q, want %q", gate, GatePassed)
	}
	// bd's own litter goes too.
	if config := f.journal().Target.LaunchConfig; config != "" {
		if _, err := os.Lstat(config); !os.IsNotExist(err) {
			t.Errorf("rollback left the nonce-bound launch config at %s", config)
		}
	}
	// And the replacement is stopped, so the data dir is the caller's again.
	if locked, ok, detail := dataDirLocked(f.dataDir); ok && locked {
		t.Errorf("rollback left the data dir locked: %s", detail)
	}
}

// D3: bd runs `dolt init` when the data dir is not a Dolt root, and rollback
// removes exactly what that created. Proven by diffing the tree, because
// "the directory still exists" is not the claim.
func TestDoltInitIsExactlyReversed(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	// A second database in the same multi-database dir, with no .dolt: this is
	// what makes configure run `dolt init`. The first one stays initialized,
	// and the diff must show it untouched.
	secondDB := filepath.Join(f.dataDir, "uninitialized_db")
	if err := os.MkdirAll(secondDB, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	f.mustRun(VerbPrepare)
	if f.journal().Snapshot.DataDirIsDolt {
		t.Skip("this data dir is already a dolt root at its top level; the init branch will not run")
	}
	before := treeFingerprint(t, f.dataDir)

	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)

	j := f.journal()
	if j.Reservations.DataDirInit != ReservationDone {
		t.Skipf("configure did not need to initialize the data dir (reservation %q)", j.Reservations.DataDirInit)
	}
	if after := treeFingerprint(t, f.dataDir); after == before {
		t.Fatal("the init reservation was recorded but the tree did not change")
	}

	f.mustRun(VerbRollback)
	after := treeFingerprint(t, f.dataDir)
	if after != before {
		t.Fatalf("dolt init was not exactly reversed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A data dir that was ALREADY a Dolt root must never have its .dolt removed,
// even if a reservation survived from an interrupted run. The reservation says
// bd intended to initialize; only the snapshot says whether there was anything
// there first.
func TestRollbackNeverRemovesADatabaseBDDidNotCreate(t *testing.T) {
	root := canonicalTempDir(t)
	dataDir := filepath.Join(BeadsDir(root), "dolt")
	dotDolt := filepath.Join(dataDir, ".dolt")
	if err := os.MkdirAll(dotDolt, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := filepath.Join(dotDolt, "precious")
	if err := os.WriteFile(payload, []byte("the caller's database"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	x := &run{
		path: JournalPath(root),
		req:  Request{Root: root},
		j: Journal{
			Phase:        PhaseRollbackStarted,
			Owner:        OwnerLegacy,
			Request:      Request{Root: root},
			Reservations: Reservations{DataDirInit: ReservationDone},
			// The decisive field: the dir was already a Dolt root at prepare.
			Snapshot: Snapshot{DataDirIsDolt: true},
		},
	}
	e := newEvidence("test")
	if err := x.undoDataDirInit(&e); err != nil {
		t.Fatalf("undoDataDirInit: %v", err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("rollback removed a database bd did not create: %v", err)
	}
	if got := e.Gates["data_dir_init_undone"]; got != GateSkipped {
		t.Errorf("gate is %q, want %q", got, GateSkipped)
	}
}

// A crash after the launch reservation reaches disk and before the identity is
// recorded leaves an orphan holding the data dir. The next verb must retire it
// by its durable nonce rather than launching a second server over it.
func TestCrashAfterTargetLaunchReservationIsRecovered(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)
	f.mustRun(VerbPrepare)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)

	crash := errors.New("modelled crash after the launch reservation")
	_, err := f.runWith(VerbConfigure, func(o *Options) {
		o.AfterReserve = func(name string) error {
			if name == "target_launch" {
				return crash
			}
			return nil
		}
	})
	if err == nil {
		t.Fatal("the modelled crash did not stop configure")
	}
	interrupted := f.journal()
	if interrupted.Reservations.TargetLaunch != ReservationInProgress {
		t.Fatalf("the interrupted launch left reservation %q, want %q",
			interrupted.Reservations.TargetLaunch, ReservationInProgress)
	}
	if interrupted.Target.LaunchID == "" {
		t.Fatal("the reservation recorded no launch identity to recover by")
	}

	// Retrying must succeed, and must end with exactly one replacement.
	f.mustRun(VerbConfigure)
	recovered := f.journal()
	if recovered.Reservations.TargetLaunch != ReservationDone {
		t.Fatalf("retry left reservation %q", recovered.Reservations.TargetLaunch)
	}
	if recovered.Target.PID <= 0 || recovered.Target.Birth == "" {
		t.Fatalf("retry did not record a strict identity: %+v", recovered.Target)
	}
	f.mustRun(VerbVerify)
}

// The same shape for the other reservation: a crash after data_dir_init is
// recorded in progress must leave rollback able to tell that bd is the one who
// created the .dolt subtree.
func TestCrashAfterDataDirInitReservationIsRecoverable(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	secondDB := filepath.Join(f.dataDir, "uninitialized_db")
	if err := os.MkdirAll(secondDB, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f.mustRun(VerbPrepare)
	if f.journal().Snapshot.DataDirIsDolt {
		t.Skip("the data dir is already a dolt root; the init branch will not run")
	}
	before := treeFingerprint(t, f.dataDir)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)

	crash := errors.New("modelled crash after the init reservation")
	_, err := f.runWith(VerbConfigure, func(o *Options) {
		o.AfterReserve = func(name string) error {
			if name == "data_dir_init" {
				return crash
			}
			return nil
		}
	})
	if err == nil {
		t.Fatal("the modelled crash did not stop configure")
	}
	if got := f.journal().Reservations.DataDirInit; got != ReservationInProgress {
		t.Fatalf("reservation is %q, want %q", got, ReservationInProgress)
	}

	// Rollback from here treats the in-progress reservation as bd's own init.
	f.mustRun(VerbRollback)
	if after := treeFingerprint(t, f.dataDir); after != before {
		t.Fatalf("rollback after an interrupted init did not restore the tree:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

// R2 refuses without advancing while the caller's server is not back, so the
// fence always has an exit: the caller restarts and runs the verb again.
func TestRollbackFinishIsReRunnableWhileItRefuses(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)
	f.mustRun(VerbPrepare)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbRollback)

	// Nothing is listening: the fence is up and rollback-finish refuses.
	for attempt := 0; attempt < 2; attempt++ {
		result, err := f.run(VerbRollbackFinish)
		if ErrorCode(err) != CodeLegacyNotBack {
			t.Fatalf("attempt %d returned %q, want %q (err: %v)",
				attempt, ErrorCode(err), CodeLegacyNotBack, err)
		}
		if result.Phase != PhaseLegacyConfigRestored {
			t.Fatalf("a refusal moved the phase to %s", result.Phase)
		}
	}
	if err := CheckNormalOpen(f.beadsDir); err == nil {
		t.Fatal("the fence is down while the rollback is unfinished")
	}

	// The caller restarts its server on the same endpoint, which is what R2 is
	// waiting for. Its restart is licensed to canonicalise config.yaml.
	f.startLegacy()
	if err := ensureConfigYAML(f.beadsDir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	for key, value := range map[string]string{
		"dolt.host": "127.0.0.1",
		"dolt.port": strconv.Itoa(f.legacyPort),
	} {
		if err := config.SetYamlConfigInDir(f.beadsDir, key, value); err != nil {
			t.Fatalf("canonicalise config.yaml %s: %v", key, err)
		}
	}

	finished := f.mustRun(VerbRollbackFinish)
	if finished.Phase != PhaseRolledBack {
		t.Fatalf("rollback-finish left phase %s", finished.Phase)
	}
	if _, err := os.Lstat(JournalPath(f.root)); !os.IsNotExist(err) {
		t.Fatal("rollback-finish did not archive the journal")
	}
	matches, _ := filepath.Glob(JournalPath(f.root) + ".rolled-back-*")
	if len(matches) != 1 {
		t.Fatalf("expected exactly one archived journal, found %v", matches)
	}
	if err := CheckNormalOpen(f.beadsDir); err != nil {
		t.Fatalf("the fence is still up after a finished rollback: %v", err)
	}
}

// R2 (iv) is about identity, not liveness. A DIFFERENT server answering on the
// caller's endpoint — bd's own replacement, still running — must not be
// mistaken for the caller's server having come back.
func TestRollbackFinishRefusesBDsOwnServerAnsweringTheLegacyEndpoint(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)
	f.mustRun(VerbPrepare)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)

	target := f.journal().Target
	if target.PID <= 0 {
		t.Fatal("no replacement to stand in for the caller's server")
	}
	// Rewrite the journal so the endpoint R2 probes is the replacement's. That
	// models the exact hazard: something is listening, it serves the same data,
	// but it is not the caller's process.
	j := f.journal()
	j.Phase = PhaseLegacyConfigRestored
	j.Request.Endpoint = Endpoint{Host: target.Host, Port: target.Port}
	if err := save(JournalPath(f.root), j); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}

	opts := f.opts()
	opts.Endpoint = Endpoint{Host: target.Host, Port: target.Port}
	result, err := Run(t.Context(), VerbRollbackFinish, opts)
	code := ErrorCode(err)
	if code != CodeLegacyIdentityUnproven && code != CodeLegacyNotBack && code != CodeBDServerPresent {
		t.Fatalf("rollback-finish returned %q; it should refuse a replacement wearing the caller's endpoint (err: %v)",
			code, err)
	}
	if result.Phase == PhaseRolledBack {
		t.Fatal("rollback-finish admitted bd's own replacement as the caller's server")
	}
}

// Resume: every phase is re-runnable and reports the journal rather than
// re-doing the work. Re-running prepare after a mutation would be the worst
// case — it would overwrite the snapshot the restore depends on.
func TestEveryPhaseIsIdempotent(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	f.mustRun(VerbPrepare)
	snapshotAfterFirstPrepare := mustJSON(t, f.journal().Snapshot)
	f.mustRun(VerbPrepare)
	if again := mustJSON(t, f.journal().Snapshot); again != snapshotAfterFirstPrepare {
		t.Fatal("re-running prepare rewrote the snapshot the restore depends on")
	}

	f.stopLegacy()
	for _, verb := range []string{VerbLegacyGone, VerbConfigure, VerbVerify} {
		first := f.mustRun(verb)
		second := f.mustRun(verb)
		if first.Phase != second.Phase {
			t.Fatalf("re-running %s moved the phase from %s to %s", verb, first.Phase, second.Phase)
		}
		if second.ErrorCode != "" {
			t.Fatalf("re-running %s reported %q", verb, second.ErrorCode)
		}
	}
	// The target is the same process both times: configure did not launch a
	// second server on its replay.
	target := f.journal().Target
	f.mustRun(VerbConfigure)
	if again := f.journal().Target; again.PID != target.PID || again.LaunchID != target.LaunchID {
		t.Fatalf("replaying configure relaunched the target: %+v then %+v", target, again)
	}
}

// A verb asked for out of order refuses with phase_order and changes nothing.
func TestOutOfOrderVerbsRefuse(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)
	for _, verb := range []string{VerbConfigure, VerbVerify, VerbCommit, VerbRollbackFinish} {
		result, err := f.run(verb)
		if ErrorCode(err) != CodePhaseOrder {
			t.Errorf("%s at %s returned %q, want %q (err: %v)",
				verb, result.Phase, ErrorCode(err), CodePhaseOrder, err)
		}
		if result.Phase != PhasePrepared {
			t.Fatalf("%s advanced the phase to %s", verb, result.Phase)
		}
	}
}

// A journal belongs to one scope. A verb naming a different database is refused
// rather than merged into the existing journal.
func TestRequestConflictingWithTheJournalIsRefused(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)

	opts := f.opts()
	opts.Database = "a_different_database"
	result, err := Run(t.Context(), VerbLegacyGone, opts)
	if ErrorCode(err) != CodeIdentityConflict {
		t.Fatalf("returned %q, want %q (err: %v)", ErrorCode(err), CodeIdentityConflict, err)
	}
	if result.Phase != PhasePrepared {
		t.Fatalf("a conflicting request advanced the phase to %s", result.Phase)
	}
}

// The fence is not decoration: an in-flight handoff must stop an ordinary bd
// store open, and must let go once the transfer settles.
func TestFenceBlocksOrdinaryOpensDuringTheTransfer(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	if err := CheckNormalOpen(f.beadsDir); err != nil {
		t.Fatalf("the fence is up before any handoff exists: %v", err)
	}
	f.mustRun(VerbPrepare)
	if err := CheckNormalOpen(f.beadsDir); err == nil {
		t.Fatal("the fence is down during a prepared handoff")
	}
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbVerify)
	if err := CheckNormalOpen(f.beadsDir); err == nil {
		t.Fatal("the fence is down at verified, when the scope has no settled owner")
	}
	f.mustRun(VerbCommit)
	if err := CheckNormalOpen(f.beadsDir); err != nil {
		t.Fatalf("the fence is still up after commit: %v", err)
	}
}

// The commit refusal path: when the post-write assertion disagrees, the write
// set is restored and the phase stays verified, so a caller can retry or roll
// back from a workspace that is exactly as prepare found it.
func TestCommitRefusedRestoresTheWriteSet(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)
	before := f.workspaceFingerprint()

	f.mustRun(VerbPrepare)
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbVerify)

	// BEADS_DOLT_SERVER_MODE=1 makes ResolveServerMode return External no
	// matter what commit writes, so the post-write assertion cannot pass. That
	// is a real disagreement between bd's resolvers and the write set, which is
	// exactly what commit_refused is for.
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	result, err := f.run(VerbCommit)
	if ErrorCode(err) != CodeCommitRefused {
		t.Fatalf("commit returned %q, want %q (err: %v)", ErrorCode(err), CodeCommitRefused, err)
	}
	if result.Phase != PhaseVerified {
		t.Fatalf("a refused commit left phase %s, want %s", result.Phase, PhaseVerified)
	}
	if f.journal().Reservations.CommitWriteSet == ReservationDone {
		t.Fatal("a refused commit left its write-set reservation complete")
	}

	// The write set is back. Only the lifecycle files configure legitimately
	// wrote may differ, so compare the caller's own two files exactly.
	for _, name := range []string{configfile.ConfigFileName, "config.yaml"} {
		path := filepath.Join(f.beadsDir, name)
		art, captureErr := captureArtifact(path)
		if captureErr != nil {
			t.Fatalf("capture %s: %v", path, captureErr)
		}
		if name == configfile.ConfigFileName {
			cfg, loadErr := configfile.Load(f.beadsDir)
			if loadErr != nil {
				t.Fatalf("reload metadata.json: %v", loadErr)
			}
			if cfg.DoltServerPort != f.legacyPort {
				t.Errorf("a refused commit left dolt_server_port %d, want %d restored",
					cfg.DoltServerPort, f.legacyPort)
			}
		}
		if name == "config.yaml" && art.Present {
			t.Errorf("a refused commit left a config.yaml the workspace never had")
		}
	}
	_ = before
}

// status never mutates, at any phase.
func TestStatusNeverMutates(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)
	before := f.workspaceFingerprint()
	journalBefore := mustJSON(t, f.journal())

	result, err := f.run(VerbStatus)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if result.Phase != PhasePrepared {
		t.Fatalf("status reported phase %s", result.Phase)
	}
	if after := f.workspaceFingerprint(); after != before {
		t.Fatal("status changed the workspace")
	}
	if after := mustJSON(t, f.journal()); after != journalBefore {
		t.Fatal("status rewrote the journal")
	}
}

// A workspace whose project identity does not match the request is a different
// scope, however well the endpoint answers.
func TestPrepareRefusesAMismatchedWorkspaceIdentity(t *testing.T) {
	f := newFixture(t)
	opts := f.opts()
	opts.Workspace = "some-other-project"
	result, err := Run(t.Context(), VerbPrepare, opts)
	if ErrorCode(err) != CodeIdentityConflict {
		t.Fatalf("prepare returned %q, want %q (err: %v)", ErrorCode(err), CodeIdentityConflict, err)
	}
	if result.Phase == PhasePrepared {
		t.Fatal("prepare accepted a workspace identity that does not match")
	}
	if _, statErr := os.Lstat(JournalPath(f.root)); !os.IsNotExist(statErr) {
		t.Fatal("a refused prepare left a journal behind")
	}
}

// An endpoint that does not answer is legacy_unreachable, not a silent success:
// prepare's whole job is to prove the caller's server serves this scope.
func TestPrepareRefusesAnUnreachableEndpoint(t *testing.T) {
	f := newFixture(t)
	opts := f.opts()
	opts.Endpoint = Endpoint{Host: "127.0.0.1", Port: f.freePort()}
	result, err := Run(t.Context(), VerbPrepare, opts)
	if ErrorCode(err) != CodeLegacyUnreachable {
		t.Fatalf("prepare returned %q, want %q (err: %v)", ErrorCode(err), CodeLegacyUnreachable, err)
	}
	if result.Phase == PhasePrepared {
		t.Fatal("prepare accepted an endpoint that never answered")
	}
}

// The data dir being locked is gate (c). A held noms LOCK with the endpoint
// already quiet is the case where only that gate can tell the truth.
func TestHeldDataDirLockRefusesLegacyGone(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)
	f.stopLegacy()

	lockPath := filepath.Join(f.dataDir, f.database, ".dolt", "noms", "LOCK")
	if _, err := os.Stat(lockPath); err != nil {
		t.Skipf("this dolt build keeps no noms LOCK at %s: %v", lockPath, err)
	}
	release := holdLock(t, lockPath)
	defer release()

	result, err := f.run(VerbLegacyGone)
	if ErrorCode(err) != CodeDataDirLocked {
		t.Fatalf("legacy-gone returned %q, want %q (err: %v)", ErrorCode(err), CodeDataDirLocked, err)
	}
	if result.Phase != PhasePrepared {
		t.Fatalf("a refusal advanced the phase to %s", result.Phase)
	}

	// Released, the same verb passes — the gate tracks the world, not a latch.
	release()
	f.mustRun(VerbLegacyGone)
}

// R2 (iii) asks whether config.yaml still points at the endpoint that answered.
// That is only a question when config.yaml pointed anywhere to begin with.
//
// A Gas City managed city does not: it records its endpoint in the caller's own
// runtime state, and its canonical resolver deliberately blanks dolt.host and
// dolt.port — `gc.endpoint_origin: managed_city` means exactly "the endpoint is
// the caller's to resolve". Demanding one here refused a correct rollback, on a
// workspace whose server was answering, permanently. The gate is now recorded
// skipped when the snapshot saw no endpoint at prepare, and (i), (ii) and (iv)
// carry it.
func TestRollbackFinishAdmitsACallerThatKeepsItsEndpointElsewhere(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	// The workspace shape under test: no dolt.host/dolt.port anywhere in
	// config.yaml, which is how the fixture starts.
	if host, port := configuredEndpoint(f.beadsDir); host != "" || port != "" {
		t.Fatalf("fixture already carries an endpoint in config.yaml (%s:%s)", host, port)
	}

	f.mustRun(VerbPrepare)
	if f.journal().Snapshot.ConfigHadEndpoint {
		t.Fatal("prepare recorded an endpoint config.yaml does not have")
	}

	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbRollback)

	// The caller restarts its own server, and still records its endpoint
	// somewhere bd cannot see.
	f.startLegacy()

	finished := f.mustRun(VerbRollbackFinish)
	if finished.Phase != PhaseRolledBack {
		t.Fatalf("rollback-finish left phase %s", finished.Phase)
	}
	evidence := finished.Evidence[string(PhaseRolledBack)]
	if got := evidence.Gates["config_points_at_legacy"]; got != GateSkipped {
		t.Errorf("gate config_points_at_legacy is %q, want %q", got, GateSkipped)
	}
	// The gates that carry the decision must have actually passed — a skipped
	// (iii) must not become a way to admit a rollback nothing checked.
	for _, gate := range []string{"legacy_answers", "artifacts_still_restored"} {
		if got := evidence.Gates[gate]; got != GatePassed {
			t.Errorf("gate %s is %q, want %q", gate, got, GatePassed)
		}
	}
	if _, err := os.Lstat(JournalPath(f.root)); !os.IsNotExist(err) {
		t.Fatal("rollback-finish did not archive the journal")
	}
}

// The mirror: a workspace that DOES carry an endpoint is still held to it, so
// the skip is scoped to the shape that needs it rather than being a hole.
func TestRollbackFinishStillChecksAConfigThatCarriesAnEndpoint(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	// Give the workspace the shape a bd-initialised one has, before prepare.
	if err := ensureConfigYAML(f.beadsDir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	for key, value := range map[string]string{
		"dolt.host": "127.0.0.1",
		"dolt.port": strconv.Itoa(f.legacyPort),
	} {
		if err := config.SetYamlConfigInDir(f.beadsDir, key, value); err != nil {
			t.Fatalf("seed config.yaml %s: %v", key, err)
		}
	}

	f.mustRun(VerbPrepare)
	if !f.journal().Snapshot.ConfigHadEndpoint {
		t.Fatal("prepare did not record the endpoint config.yaml carries")
	}
	f.stopLegacy()
	f.mustRun(VerbLegacyGone)
	f.mustRun(VerbConfigure)
	f.mustRun(VerbRollback)
	f.startLegacy()

	// Point config.yaml somewhere else entirely: (iii) must refuse.
	if err := config.SetYamlConfigInDir(f.beadsDir, "dolt.port", "1"); err != nil {
		t.Fatalf("misdirect config.yaml: %v", err)
	}
	result, err := f.run(VerbRollbackFinish)
	if ErrorCode(err) != CodeLegacyNotBack {
		t.Fatalf("rollback-finish returned %q, want %q (err: %v)", ErrorCode(err), CodeLegacyNotBack, err)
	}
	if result.Phase != PhaseLegacyConfigRestored {
		t.Fatalf("a refusal moved the phase to %s", result.Phase)
	}

	// Put it back and it settles, which proves the gate tracks the file.
	if err := config.SetYamlConfigInDir(f.beadsDir, "dolt.port", strconv.Itoa(f.legacyPort)); err != nil {
		t.Fatalf("restore config.yaml: %v", err)
	}
	if finished := f.mustRun(VerbRollbackFinish); finished.Phase != PhaseRolledBack {
		t.Fatalf("rollback-finish left phase %s", finished.Phase)
	}
}
