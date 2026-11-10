package schema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectSharedGateProbe mocks the probe sequence every gate invocation issues
// before it can decide anything: CurrentVersion, PendingVersions, and the
// dolt_remotes count.
func expectSharedGateProbe(mock sqlmock.Sqlmock, current int, remotes int) {
	expectGateCurrentVersion(mock, current) // CurrentVersion
	expectGateCurrentVersion(mock, current) // PendingVersions -> pending exists
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(remotes))
}

func sharedGate(t *testing.T, mock sqlmock.Sqlmock, db DBConn) error {
	t.Helper()
	err := CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Fatalf("unmet expectations: %v", merr)
	}
	return err
}

// resetConsent clears both process-local consent flags around a test case.
// They are package-level by design (set once in the root pre-run), so a test
// that leaves one set silently unlocks every later case.
func resetConsent(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetForceAllowRemoteMigrate(false)
		SetSharedMigrateConsent(false)
	})
}

// TestSharedStoreGateNoRemote covers the gastownhall/beads#5920 arm: a shared
// database with NO remote, where the ordinary (embedded) gate allows a silent
// in-place migration. On a shared store that promotes the schema cursor for
// every co-resident client at once, so it refuses without explicit consent.
func TestSharedStoreGateNoRemote(t *testing.T) {
	t.Setenv(SmartGateEnv, "0") // no remote, so the smart router never runs anyway
	latest := LatestVersion()

	t.Run("pending migrations, no remote, no consent → refused", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 0)

		var gateErr *RemoteMigrateGateError
		if err := sharedGate(t, mock, db); !errors.As(err, &gateErr) {
			t.Fatalf("expected *RemoteMigrateGateError, got %v", err)
		}
		if gateErr.Decision != gateDecisionSharedNoRemote {
			t.Fatalf("Decision = %q, want %q", gateErr.Decision, gateDecisionSharedNoRemote)
		}
		if gateErr.CurrentVersion != 1 || gateErr.LatestVersion != latest || gateErr.Pending <= 0 {
			t.Errorf("gate error = %+v, want current 1, latest %d, pending > 0", gateErr, latest)
		}

		msg := gateErr.UserMessage()
		for _, want := range []string{
			"lock out every co-resident bd client",
			"bd migrate schema",
			"Read commands keep working",
			AllowRemoteMigrateEnv + "=1",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("UserMessage missing %q:\n%s", want, msg)
			}
		}
		// The #4259 remedies do not apply with no remote: there is one copy of
		// the database, so there is nothing to adopt and nothing to push.
		for _, absent := range []string{"bd bootstrap", "bd dolt push"} {
			if strings.Contains(msg, absent) {
				t.Errorf("UserMessage must not offer %q with no remote:\n%s", absent, msg)
			}
		}

		if hint := gateErr.EscapeHint(); hint != "bd migrate schema" {
			t.Errorf("EscapeHint = %q, want %q", hint, "bd migrate schema")
		}
		opts := gateErr.Options()
		if len(opts) != 1 || opts[0].ID != "migrate-shared" {
			t.Fatalf("Options = %+v, want a single migrate-shared option", opts)
		}
		if opts[0].When == "" || opts[0].Risk == "" {
			t.Errorf("migrate-shared option must carry its precondition and risk: %+v", opts[0])
		}
		if directive := gateErr.AgentDirective(); !strings.Contains(directive, "do NOT auto-run") {
			t.Errorf("AgentDirective must forbid auto-running the migration: %q", directive)
		}
	})

	t.Run("fresh database still migrates — creating a database is consent", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 0)

		if err := sharedGate(t, mock, db); err != nil {
			t.Fatalf("fresh shared database must be allowed, got %v", err)
		}
	})

	t.Run("nothing pending is allowed", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, latest) // CurrentVersion
		expectGateCurrentVersion(mock, latest) // PendingVersions -> none

		if err := sharedGate(t, mock, db); err != nil {
			t.Fatalf("converged shared database must be allowed, got %v", err)
		}
	})

	t.Run("verb consent unlocks with a warning", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 0)
		SetSharedMigrateConsent(true)

		var err error
		stderr := captureStderr(t, func() { err = sharedGate(t, mock, db) })
		if err != nil {
			t.Fatalf("bd migrate schema consent must unlock, got %v", err)
		}
		if !strings.Contains(stderr, "bd migrate schema") || !strings.Contains(stderr, "#5920") {
			t.Errorf("expected a shared-store warning naming the verb, got %q", stderr)
		}
	})

	t.Run("--force unlocks with a warning", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 0)
		SetForceAllowRemoteMigrate(true)

		var err error
		stderr := captureStderr(t, func() { err = sharedGate(t, mock, db) })
		if err != nil {
			t.Fatalf("--force must unlock, got %v", err)
		}
		if !strings.Contains(stderr, "bd migrate --force") {
			t.Errorf("expected a warning naming --force, got %q", stderr)
		}
	})

	t.Run("env unlocks with a warning", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE"} {
			resetConsent(t)
			t.Setenv(AllowRemoteMigrateEnv, v)
			db, mock, _ := sqlmock.New()
			expectSharedGateProbe(mock, 1, 0)

			var err error
			stderr := captureStderr(t, func() { err = sharedGate(t, mock, db) })
			if err != nil {
				t.Fatalf("%s=%s must unlock, got %v", AllowRemoteMigrateEnv, v, err)
			}
			if !strings.Contains(stderr, AllowRemoteMigrateEnv+"="+v) {
				t.Errorf("%s=%s: expected a warning naming the variable, got %q", AllowRemoteMigrateEnv, v, stderr)
			}
			db.Close()
		}
	})

	t.Run("unparseable env stays locked with a hint", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "yes")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 0)

		var gateErr *RemoteMigrateGateError
		if err := sharedGate(t, mock, db); !errors.As(err, &gateErr) {
			t.Fatalf("expected *RemoteMigrateGateError, got %v", err)
		}
		if gateErr.UnrecognizedEnv != "yes" {
			t.Errorf("UnrecognizedEnv = %q, want %q", gateErr.UnrecognizedEnv, "yes")
		}
		if !strings.Contains(gateErr.UserMessage(), "not recognized") {
			t.Errorf("UserMessage missing the unrecognized-value hint:\n%s", gateErr.UserMessage())
		}
	})

	// Invariant 1 of the decision: embedded/local databases keep auto-migrating
	// on a version bump. Same state, non-shared caller, no refusal.
	t.Run("embedded (non-shared) caller is unaffected", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 0)

		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("embedded no-remote open must still auto-migrate, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestSharedStoreGateSuppressesSmartAutoArms covers the remote-configured half:
// the #4259 flow is unchanged except that the smart gate's two auto-EXECUTE
// arms never fire on a shared store. The first-mover arm firing here IS the
// #5920 mechanism.
func TestSharedStoreGateSuppressesSmartAutoArms(t *testing.T) {
	floor := LastNonDeterministicMigration
	latest := LatestVersion()

	t.Run("first-mover auto-migrate is suppressed to a blunt stop", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		hashes := map[int]string{floor - 1: "h1", floor: "h2"}
		expectSmartRemoteRead(mock, hashes, hashes)

		err := CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("shared store must refuse the first-mover migrate, got %v", err)
		}
		if gateErr.Decision != "" {
			t.Errorf("Decision = %q, want the blunt stop", gateErr.Decision)
		}
		if gateErr.FallbackReason != fallbackReasonSharedStore {
			t.Errorf("FallbackReason = %q, want %q", gateErr.FallbackReason, fallbackReasonSharedStore)
		}
		if !strings.Contains(gateErr.UserMessage(), "lock out co-resident clients") {
			t.Errorf("UserMessage should explain the shared-store suppression:\n%s", gateErr.UserMessage())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("embedded caller in the same state still auto-migrates", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		hashes := map[int]string{floor - 1: "h1", floor: "h2"}
		expectSmartRemoteRead(mock, hashes, hashes)

		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("embedded safe first-mover must still be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	// A refusal that came from a shared store carries consequences its #4259
	// decisions were never annotated for: every remedy lands on the database
	// the whole server is serving. adopt-ff is the sharp case — its risk is
	// otherwise literally "none", which an agent would read as free.
	t.Run("shared refusals annotate the fleet-wide risk", func(t *testing.T) {
		for _, decision := range []string{gateDecisionAdopt, gateDecisionAdoptFastForward} {
			shared := (&RemoteMigrateGateError{Decision: decision, Shared: true}).Options()
			if len(shared) != 1 {
				t.Fatalf("%s: expected one option, got %+v", decision, shared)
			}
			if !strings.Contains(shared[0].Risk, "co-resident") {
				t.Errorf("%s: shared risk must name the co-resident consequence, got %q", decision, shared[0].Risk)
			}
			local := (&RemoteMigrateGateError{Decision: decision}).Options()
			if strings.Contains(local[0].Risk, "co-resident") {
				t.Errorf("%s: a non-shared refusal must keep its original risk, got %q", decision, local[0].Risk)
			}
		}
		// "Risk: none" is only ever correct off a shared store.
		ff := (&RemoteMigrateGateError{Decision: gateDecisionAdoptFastForward, Shared: true}).Options()
		if strings.HasPrefix(ff[0].Risk, "none") {
			t.Errorf("adopt-ff on a shared store must not report no risk, got %q", ff[0].Risk)
		}
	})

	t.Run("shared is set on the remote-backed arms too", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "0")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 1)

		err := CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected a gate error, got %v", err)
		}
		if !gateErr.Shared {
			t.Error("a refusal from the shared gate must be marked Shared, whatever its decision")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("auto fast-forward is never executed", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "1")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSmartFiringGate(mock, floor)
		local := map[int]string{floor: "h1"}
		// Exactly the fixture that DOES auto-execute on an embedded clone
		// (remote at latest, strict ancestor, clean working set) — no TOCTOU
		// re-read is expected here because the write never happens.
		remote := map[int]string{floor: "h1", latest: "h2"}
		expectSmartRemoteRead(mock, local, remote)

		fa := &fakeAdopter{ancestorResult: true, cleanResult: true, withFastForward: true}
		err := CheckSharedStoreMigrateGate(context.Background(), db, "", nil, fa.adopter())
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("shared store must refuse rather than fast-forward, got %v", err)
		}
		if gateErr.Decision != gateDecisionAdoptFastForward {
			t.Errorf("Decision = %q, want %q (the directive stays accurate, only the write is suppressed)",
				gateErr.Decision, gateDecisionAdoptFastForward)
		}
		if fa.ffCalls != 0 {
			t.Errorf("FastForward calls = %d, want 0 on a shared store", fa.ffCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	// Verb consent is scoped to the no-remote arm on purpose: with a remote,
	// #4259's cross-clone fork risk still demands the designated-migrator
	// confirmation.
	t.Run("verb consent does not unlock the remote-backed arm", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "0")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 1)
		SetSharedMigrateConsent(true)

		err := CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
		if !IsRemoteMigrateGateError(err) {
			t.Fatalf("verb consent must not unlock a remote-backed database, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("--force still unlocks the remote-backed arm", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(SmartGateEnv, "0")
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectSharedGateProbe(mock, 1, 1)
		SetForceAllowRemoteMigrate(true)

		var err error
		_ = captureStderr(t, func() {
			err = CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
		})
		if err != nil {
			t.Fatalf("--force must unlock a remote-backed shared database, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}
