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

// readsRefusedGateCases is one refusal per decision the gate can return,
// for the gastownhall/beads#6572 read-refusal tests.
func readsRefusedGateCases() map[string]*RemoteMigrateGateError {
	base := func(e RemoteMigrateGateError) *RemoteMigrateGateError {
		e.CurrentVersion, e.LatestVersion, e.Pending = 59, 66, 7
		return &e
	}
	return map[string]*RemoteMigrateGateError{
		"blunt":            base(RemoteMigrateGateError{}),
		"adopt":            base(RemoteMigrateGateError{Decision: gateDecisionAdopt}),
		"adopt-ff":         base(RemoteMigrateGateError{Decision: gateDecisionAdoptFastForward}),
		"fork-skew":        base(RemoteMigrateGateError{Decision: gateDecisionForkSkew, SkewVersions: []int{60}}),
		"shared-no-remote": base(RemoteMigrateGateError{Decision: gateDecisionSharedNoRemote, Shared: true}),
		"data-behind":      base(RemoteMigrateGateError{FallbackReason: fallbackReasonDataBehind}),
		"data-behind proxied shared": base(RemoteMigrateGateError{
			FallbackReason: fallbackReasonDataBehind, Shared: true, Proxied: true,
		}),
	}
}

// TestRemoteMigrateGateError_ReadsRefused pins the classification a refused
// read-through relies on (gastownhall/beads#6572): with ReadsRefused attached
// the error is still the gate's, with its decision, and is also the
// *SchemaBehindError naming what is missing; without it nothing unwraps and
// the one-line refusal is unchanged.
func TestRemoteMigrateGateError_ReadsRefused(t *testing.T) {
	for name, gateErr := range readsRefusedGateCases() {
		t.Run(name, func(t *testing.T) {
			plain := gateErr.Error()
			if u := gateErr.Unwrap(); u != nil {
				t.Fatalf("Unwrap() without ReadsRefused = %v, want nil", u)
			}
			if IsSchemaBehindError(gateErr) {
				t.Fatal("a gate refusal without ReadsRefused classifies as *SchemaBehindError")
			}
			if strings.Contains(plain, "reads are refused") {
				t.Fatalf("Error() without ReadsRefused claims reads are refused: %q", plain)
			}

			refused := *gateErr
			refused.ReadsRefused = &SchemaBehindError{DBVersion: 59, BinaryVersion: 66, Missing: []string{"leases", "wisps.storage_class"}}
			var err error = &refused
			var gotGate *RemoteMigrateGateError
			if !errors.As(err, &gotGate) || gotGate.Decision != gateErr.Decision {
				t.Fatalf("errors.As(gate) = %v, want the gate refusal with Decision %q", gotGate, gateErr.Decision)
			}
			var behind *SchemaBehindError
			if !errors.As(err, &behind) || behind != refused.ReadsRefused {
				t.Fatalf("errors.As(behind) did not find the attached *SchemaBehindError in %v", err)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, plain) {
				t.Errorf("Error() = %q, want it to keep the refusal %q first", msg, plain)
			}
			for _, want := range []string{"reads are refused too", "schema v59 lacks leases, wisps.storage_class"} {
				if !strings.Contains(msg, want) {
					t.Errorf("Error() = %q, want it to contain %q", msg, want)
				}
			}
		})
	}
	var nilGate *RemoteMigrateGateError
	if u := nilGate.Unwrap(); u != nil {
		t.Errorf("nil gate Unwrap() = %v, want nil", u)
	}
}
