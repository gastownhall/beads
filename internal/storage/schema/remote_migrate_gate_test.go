package schema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const maxVersionQuery = `SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`

// expectGateCurrentVersion mocks the MAX(version) read that both CurrentVersion
// and PendingVersions issue, preceded by the be-bv7x cursor-existence probe
// that currentVersion issues before ever reading the cursor table.
func expectGateCurrentVersion(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(maxVersionQuery).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

func TestCheckRemoteMigrateGate(t *testing.T) {
	// These tests cover the blunt #4515 gate; pin the (default-on) smart
	// router off so no cached-remote reads hit the sqlmock expectations.
	t.Setenv(SmartGateEnv, "0")
	latest := LatestVersion()

	// expectFiringGate mocks the probe sequence for a behind, remote-backed
	// database — the only state in which the gate (and the escape hatch) acts.
	expectFiringGate := func(mock sqlmock.Sqlmock) {
		expectGateCurrentVersion(mock, 1) // CurrentVersion
		expectGateCurrentVersion(mock, 1) // PendingVersions -> pending exists
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	}

	t.Run("programmatic override (SetForceAllowRemoteMigrate) bypasses gate", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0") // env-var escape hatch disabled
		SetForceAllowRemoteMigrate(true)
		defer SetForceAllowRemoteMigrate(false) // reset so subsequent tests are unaffected
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		expectFiringGate(mock)
		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("SetForceAllowRemoteMigrate(true): expected nil, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("SetForceAllowRemoteMigrate: unmet expectations: %v", err)
		}
		db.Close()
	})

	t.Run("escape hatch allows migration when the gate would fire", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE"} {
			t.Setenv(AllowRemoteMigrateEnv, v)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			expectFiringGate(mock)
			if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
				t.Fatalf("%s=%s: expected nil, got %v", AllowRemoteMigrateEnv, v, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("%s=%s: unmet expectations: %v", AllowRemoteMigrateEnv, v, err)
			}
			db.Close()
		}
	})

	t.Run("unrecognized escape hatch value stays locked with a hint", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "yes")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		expectFiringGate(mock)
		gerr := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(gerr, &gateErr) {
			t.Fatalf("expected *RemoteMigrateGateError, got %v", gerr)
		}
		if gateErr.UnrecognizedEnv != "yes" {
			t.Errorf("UnrecognizedEnv = %q, want %q", gateErr.UnrecognizedEnv, "yes")
		}
		if msg := gateErr.UserMessage(); !strings.Contains(msg, "not recognized") {
			t.Errorf("UserMessage missing unrecognized-value hint:\n%s", msg)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("fresh database (version 0) is allowed", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 0)
		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("fresh DB should be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("already at latest (no pending) is allowed", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, latest) // CurrentVersion
		expectGateCurrentVersion(mock, latest) // PendingVersions -> no pending
		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("at-latest DB should be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("pending migrations but no remote is allowed", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1) // CurrentVersion
		expectGateCurrentVersion(mock, 1) // PendingVersions -> pending exists
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("no-remote DB should be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("pending migrations with a remote is blocked", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1) // CurrentVersion
		expectGateCurrentVersion(mock, 1) // PendingVersions -> pending exists
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

		err := CheckRemoteMigrateGate(context.Background(), db)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected *RemoteMigrateGateError, got %v", err)
		}
		if gateErr.CurrentVersion != 1 {
			t.Errorf("CurrentVersion = %d, want 1", gateErr.CurrentVersion)
		}
		if gateErr.LatestVersion != latest {
			t.Errorf("LatestVersion = %d, want %d", gateErr.LatestVersion, latest)
		}
		if gateErr.Pending <= 0 {
			t.Errorf("Pending = %d, want > 0", gateErr.Pending)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("missing dolt_remotes table is treated as no remote", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1)
		expectGateCurrentVersion(mock, 1)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnError(errors.New("Error 1146: Table 'beads.dolt_remotes' doesn't exist"))
		if err := CheckRemoteMigrateGate(context.Background(), db); err != nil {
			t.Fatalf("missing dolt_remotes should be treated as no remote (allow), got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestCheckRemoteMigrateGateWithRemoteCheck covers the on-disk fallback used by
// server mode: a freshly (auto-)started dolt server reports an empty dolt_remotes
// table even when a remote is configured, so the gate must fall back to a probe of
// the persisted CLI remotes before allowing migration (gastownhall/beads#4268).
func TestCheckRemoteMigrateGateWithRemoteCheck(t *testing.T) {
	// Blunt-gate coverage; keep the smart router out of the mock expectations.
	t.Setenv(SmartGateEnv, "0")
	t.Run("no SQL remote but fallback reports a remote is blocked", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1) // CurrentVersion
		expectGateCurrentVersion(mock, 1) // PendingVersions -> pending exists
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

		err := checkRemoteMigrateGate(context.Background(), db, "", func() bool { return true }, nil, false)
		var gateErr *RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("expected *RemoteMigrateGateError when the disk fallback reports a remote, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("no SQL remote and fallback reports none is allowed", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1)
		expectGateCurrentVersion(mock, 1)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

		if err := checkRemoteMigrateGate(context.Background(), db, "", func() bool { return false }, nil, false); err != nil {
			t.Fatalf("no remote anywhere should be allowed, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("SQL remote present short-circuits the fallback", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, 1)
		expectGateCurrentVersion(mock, 1)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

		called := false
		err := checkRemoteMigrateGate(context.Background(), db, "", func() bool {
			called = true
			return false
		}, nil, false)
		if !IsRemoteMigrateGateError(err) {
			t.Fatalf("expected gate error when dolt_remotes already shows a remote, got %v", err)
		}
		if called {
			t.Error("fallback must not run when dolt_remotes already reports a remote")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("fallback not consulted when there are no pending migrations", func(t *testing.T) {
		t.Setenv(AllowRemoteMigrateEnv, "0")
		latest := LatestVersion()
		db, mock, _ := sqlmock.New()
		defer db.Close()
		expectGateCurrentVersion(mock, latest) // CurrentVersion
		expectGateCurrentVersion(mock, latest) // PendingVersions -> none

		called := false
		err := checkRemoteMigrateGate(context.Background(), db, "", func() bool {
			called = true
			return true
		}, nil, false)
		if err != nil {
			t.Fatalf("at-latest DB should be allowed, got %v", err)
		}
		if called {
			t.Error("fallback (a subprocess probe) must not run when there are no pending migrations")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestRemoteMigrateGateAgentSafety locks the agent-facing safety contract at the
// source layer: the directive surfaced as the JSON hint is not runnable, and the
// runnable escape command appears only inside the conditional "migrate" option.
func TestRemoteMigrateGateAgentSafety(t *testing.T) {
	e := &RemoteMigrateGateError{CurrentVersion: 49, LatestVersion: 53, Pending: 4}

	// The directive must not be the escape command (the agent footgun).
	if e.AgentDirective() == e.EscapeHint() {
		t.Fatalf("AgentDirective must not equal the runnable escape command %q", e.EscapeHint())
	}
	if strings.Contains(e.AgentDirective(), AllowRemoteMigrateEnv+"=1 bd migrate") {
		t.Errorf("AgentDirective must not embed the runnable migrate command: %q", e.AgentDirective())
	}
	if strings.Contains(e.AgentDirective(), "bd migrate --force") {
		t.Errorf("AgentDirective must not embed the runnable --force migrate command: %q", e.AgentDirective())
	}

	opts := e.Options()
	if len(opts) != 2 {
		t.Fatalf("Options len = %d, want 2", len(opts))
	}
	byID := map[string]GateOption{}
	for _, o := range opts {
		if o.When == "" || o.Risk == "" {
			t.Errorf("option %q missing When/Risk", o.ID)
		}
		byID[o.ID] = o
	}
	migrate, ok := byID["migrate"]
	if !ok {
		t.Fatal("missing migrate option")
	}
	if _, ok := byID["adopt"]; !ok {
		t.Fatal("missing adopt option")
	}
	// The escape command must live under migrate, and nowhere else.
	foundUnderMigrate := false
	for _, c := range migrate.Commands {
		if c == e.EscapeHint() {
			foundUnderMigrate = true
		}
	}
	if !foundUnderMigrate {
		t.Errorf("migrate option commands %v must include %q", migrate.Commands, e.EscapeHint())
	}
	for _, c := range byID["adopt"].Commands {
		if c == e.EscapeHint() {
			t.Errorf("adopt option must not contain the migrate escape command")
		}
	}
}

// TestRemoteMigrateGateAdoptFastForward locks the "adopt-ff" (mybd-ae1i piece
// 2) messaging contract: it must read as strictly safer than the plain
// "adopt" decision (loss-free), must never surface the migrate escape
// command, and — like every non-blunt decision — must not carry a
// FallbackReason (it already explains itself).
func TestRemoteMigrateGateAdoptFastForward(t *testing.T) {
	e := &RemoteMigrateGateError{CurrentVersion: 49, LatestVersion: 53, Pending: 4, Decision: gateDecisionAdoptFastForward}

	if e.FallbackReason != "" {
		t.Errorf("FallbackReason should stay empty for a tailored decision, got %q", e.FallbackReason)
	}

	if e.AgentDirective() == e.EscapeHint() {
		t.Fatalf("AgentDirective must not equal the runnable escape command %q", e.EscapeHint())
	}
	if strings.Contains(e.AgentDirective(), AllowRemoteMigrateEnv+"=1 bd migrate") {
		t.Errorf("AgentDirective must not embed the runnable migrate command: %q", e.AgentDirective())
	}
	if strings.Contains(e.AgentDirective(), "bd migrate --force") {
		t.Errorf("AgentDirective must not embed the runnable --force migrate command: %q", e.AgentDirective())
	}

	opts := e.Options()
	if len(opts) != 1 {
		t.Fatalf("Options len = %d, want 1 (adopt-ff is a single unconditional-once-detected path)", len(opts))
	}
	if opts[0].ID != "adopt-fast-forward" {
		t.Errorf("Options[0].ID = %q, want %q", opts[0].ID, "adopt-fast-forward")
	}
	for _, c := range opts[0].Commands {
		if strings.Contains(c, AllowRemoteMigrateEnv) {
			t.Errorf("adopt-ff option must not surface the migrate escape command, got %q", c)
		}
	}

	msg := e.UserMessage()
	if !strings.Contains(msg, "losslessly") && !strings.Contains(msg, "loss-free") {
		t.Errorf("UserMessage should explain the fast-forward is loss-free:\n%s", msg)
	}
}

// TestRemoteMigrateGateError_ReadsRefused locks the #6572 rendering contract:
// a gate error carrying a read refusal must stop promising that reads keep
// working — in the human body and the agent directive — name the missing
// relations from the attached SchemaBehindError, and stay reachable as both
// classifications. A gate error whose reads were never checked (nil
// ReadsRefused: strict opens, write paths) keeps the promise, qualified to
// what nil means: not verified, merely not yet refused.
func TestRemoteMigrateGateError_ReadsRefused(t *testing.T) {
	newSharedGate := func(reads *SchemaBehindError) *RemoteMigrateGateError {
		return &RemoteMigrateGateError{
			CurrentVersion: 73, LatestVersion: LatestVersion(), Pending: 1,
			Decision: gateDecisionSharedNoRemote, Shared: true,
			ReadsRefused: reads,
		}
	}

	t.Run("Unwrap keeps both classifications reachable", func(t *testing.T) {
		plain := newSharedGate(nil)
		if err := plain.Unwrap(); err != nil {
			t.Errorf("Unwrap() = %v, want nil when ReadsRefused is nil", err)
		}

		behind := &SchemaBehindError{DBVersion: 73, BinaryVersion: LatestVersion(),
			Missing: []string{"leases", "leases.granted_node"}}
		carrying := newSharedGate(behind)
		if err := carrying.Unwrap(); err != behind {
			t.Errorf("Unwrap() = %v, want the carried behind error", err)
		}
		var behindOut *SchemaBehindError
		if !errors.As(error(carrying), &behindOut) || behindOut != behind {
			t.Errorf("errors.As(*SchemaBehindError) = %v, want the carried behind error through the gate", behindOut)
		}
		if behindOut.DBVersion != 73 || strings.Join(behindOut.Missing, ",") != "leases,leases.granted_node" {
			t.Errorf("behind evidence lost in the chain: %+v", behindOut)
		}
		var gateOut *RemoteMigrateGateError
		if !errors.As(error(carrying), &gateOut) || gateOut != carrying {
			t.Errorf("errors.As(*RemoteMigrateGateError) lost the gate through its own Unwrap")
		}
	})

	t.Run("unchecked reads keep a qualified continuation promise", func(t *testing.T) {
		e := newSharedGate(nil)
		msg := e.UserMessage()
		for _, want := range []string{
			"Read commands keep working",
			"as long as it can answer this binary's reads",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("UserMessage missing %q:\n%s", want, msg)
			}
		}
		if d := e.AgentDirective(); !strings.Contains(d, "Reads keep working on the current schema meanwhile") {
			t.Errorf("AgentDirective dropped the continuation promise for unchecked reads: %q", d)
		}
	})

	t.Run("a carried refusal replaces the promise and names what is missing", func(t *testing.T) {
		e := newSharedGate(&SchemaBehindError{DBVersion: 73, BinaryVersion: LatestVersion(),
			Missing: []string{"leases", "leases.granted_node"}})
		msg := e.UserMessage()
		if strings.Contains(msg, "Read commands keep working") {
			t.Errorf("UserMessage still promises reads continue against a schema they cannot query:\n%s", msg)
		}
		for _, want := range []string{
			"Reads are refused too, not just writes",
			"reads are refused too, not only writes",
			"leases, leases.granted_node",
			// The per-decision remedy survives: the refusal corrects the read
			// contract, it does not replace the shared-no-remote guidance.
			"co-resident",
			SharedConsentCommand,
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("UserMessage missing %q:\n%s", want, msg)
			}
		}
		// The note must not hand the operator a new instruction: no escape
		// hatch (it would trade the refusal for query-time unknown-column
		// failures) and no generic local migration beyond the body's own.
		for _, absent := range []string{"BD_IGNORE_SCHEMA_SKEW", "BD_IGNORE_SCHEMA_SKEW=1"} {
			if strings.Contains(e.Notes(), absent) {
				t.Errorf("Notes() must not recommend the skew escape hatch:\n%s", e.Notes())
			}
		}
		d := e.AgentDirective()
		if strings.Contains(d, "Reads keep working") {
			t.Errorf("AgentDirective still promises reads continue: %q", d)
		}
		for _, want := range []string{"Reads are refused too", "leases, leases.granted_node"} {
			if !strings.Contains(d, want) {
				t.Errorf("AgentDirective missing %q: %q", want, d)
			}
		}
	})

	t.Run("the note is arm-neutral on remote-backed decisions", func(t *testing.T) {
		// ReadsRefused is attached by lenient opens whatever the decision was,
		// so the note must not leak the shared-no-remote remedy into a
		// remote-backed arm's guidance.
		e := &RemoteMigrateGateError{
			CurrentVersion: 73, LatestVersion: LatestVersion(), Pending: 1,
			Decision: gateDecisionAdopt,
			ReadsRefused: &SchemaBehindError{DBVersion: 73, BinaryVersion: LatestVersion(),
				Missing: []string{"wisps.storage_class"}},
		}
		msg := e.UserMessage()
		for _, want := range []string{"reads are refused too, not only writes", "wisps.storage_class"} {
			if !strings.Contains(msg, want) {
				t.Errorf("UserMessage missing %q:\n%s", want, msg)
			}
		}
		if strings.Contains(msg, SharedConsentCommand) {
			t.Errorf("the reads-refused note prescribed the shared-no-remote consent verb on a remote-backed arm:\n%s", msg)
		}
	})
}
