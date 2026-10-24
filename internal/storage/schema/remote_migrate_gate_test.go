package schema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// The Error() prefix says "losslessly" on its own, so check the body: it
	// must scope that guarantee to the Dolt history, not to a re-clone.
	if body := e.userBody(); !strings.Contains(body, "no local commit would be lost") {
		t.Errorf("userBody should explain the fast-forward loses no local commit:\n%s", body)
	}
}

// TestRemoteMigrateGateAdoptGuidance pins the adoption remedy on every arm
// that tells a clone to adopt (gastownhall/beads#6687). The gate only fires on
// a clone that already has a database, and bare `bd bootstrap` reports
// "Nothing to do." there, so no arm may hand it out as the remedy. The gate
// does not know the active database's path or storage mode, so the guidance
// links the adoption procedure instead of printing filesystem commands, and
// the structured options carry no command an agent could run.
func TestRemoteMigrateGateAdoptGuidance(t *testing.T) {
	arms := []struct {
		name     string
		decision string
		fallback string
		optionID string
		wantIDs  string
	}{
		{name: "blunt default", optionID: "adopt", wantIDs: "migrate|adopt"},
		{name: "blunt shared-store", fallback: fallbackReasonSharedStore, optionID: "adopt", wantIDs: "migrate|adopt"},
		{name: "adopt", decision: gateDecisionAdopt, optionID: "adopt", wantIDs: "adopt"},
		{name: "adopt-ff", decision: gateDecisionAdoptFastForward, optionID: "adopt-fast-forward", wantIDs: "adopt-fast-forward"},
		{name: "fork-skew", decision: gateDecisionForkSkew, optionID: "reconcile-fork", wantIDs: "reconcile-fork"},
	}
	for _, arm := range arms {
		for _, shared := range []bool{false, true} {
			if arm.fallback == fallbackReasonSharedStore && !shared {
				continue // the shared-store fallback only exists on a shared store
			}
			t.Run(fmt.Sprintf("%s/shared=%v", arm.name, shared), func(t *testing.T) {
				e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 69, Pending: 16,
					Decision: arm.decision, FallbackReason: arm.fallback, Shared: shared}
				if arm.decision == gateDecisionForkSkew {
					e.SkewVersions = []int{60}
				}
				msg := e.UserMessage()
				// Prose checks ignore where the terminal block wraps.
				flat := strings.Join(strings.Fields(msg), " ")

				for _, want := range []string{
					AdoptionGuideURL,
					`"Nothing to do."`,
					"For the adoption path:",
					"previous-version export",
					"server section",
				} {
					if !strings.Contains(flat, want) {
						t.Errorf("UserMessage is missing %q:\n%s", want, msg)
					}
				}
				for _, gone := range []string{
					"Re-cloning replaces your local database",
					"Push first (`bd dolt push`)",
					"(`bd export --all -o backup.jsonl`)",
					// The gate cannot name the active database, so no
					// filesystem command may be printed for either mode.
					"mv .beads",
					"mkdir -p",
				} {
					if strings.Contains(msg, gone) {
						t.Errorf("UserMessage still carries %q:\n%s", gone, msg)
					}
				}
				for i, line := range strings.Split(msg, "\n") {
					if strings.TrimSpace(line) == "bd bootstrap" {
						t.Errorf("UserMessage prescribes bare `bd bootstrap` (line %d):\n%s", i+1, msg)
					}
				}
				if shared {
					if !strings.Contains(msg, "co-resident") {
						t.Errorf("shared UserMessage must name the co-resident consequence:\n%s", msg)
					}
					// The shared-store fallback note already names #5920, so
					// the adoption text must not repeat it there.
					if n := strings.Count(msg, "#5920"); n != 1 {
						t.Errorf("shared UserMessage names #5920 %d times, want once:\n%s", n, msg)
					}
				} else if !strings.Contains(flat, "no other bd process") {
					t.Errorf("UserMessage is missing the no-other-process precondition:\n%s", msg)
				}

				opts := e.Options()
				ids := make([]string, len(opts))
				for i, o := range opts {
					ids[i] = o.ID
				}
				if got := strings.Join(ids, "|"); got != arm.wantIDs {
					t.Fatalf("option IDs = %q, want %q", got, arm.wantIDs)
				}
				var opt GateOption
				for _, o := range opts {
					switch o.ID {
					case arm.optionID:
						opt = o
					case "migrate":
						if strings.Join(o.Commands, "|") != "bd migrate --force|bd dolt push" {
							t.Errorf("migrate option commands changed: %v", o.Commands)
						}
					}
				}
				raw, err := json.Marshal(opt)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), `"commands":[]`) {
					t.Errorf("%s option must carry an empty, non-null commands array; got %s", arm.optionID, raw)
				}
				if !strings.Contains(opt.When, AdoptionGuideURL) {
					t.Errorf("%s option When must point at the adoption procedure; got %q", arm.optionID, opt.When)
				}
				for _, want := range []string{"Nothing to do", "previous bd version"} {
					if !strings.Contains(opt.Risk, want) {
						t.Errorf("%s option Risk is missing %q; got %q", arm.optionID, want, opt.Risk)
					}
				}
				if strings.HasPrefix(opt.Risk, "none") {
					t.Errorf("%s option Risk must not claim no risk; got %q", arm.optionID, opt.Risk)
				}
				if got := strings.Contains(opt.Risk, "co-resident"); got != shared {
					t.Errorf("%s option Risk names the co-resident consequence = %v, want %v; got %q", arm.optionID, got, shared, opt.Risk)
				}

				d := e.AgentDirective()
				if arm.decision == "" {
					// The blunt directive prescribes nothing; the options carry it.
					if !strings.Contains(d, "remote_migrate_gate.options") {
						t.Errorf("default directive must still point at the options; got %q", d)
					}
				} else {
					for _, want := range []string{AdoptionGuideURL, "Nothing to do", "Do NOT move database files or run bd bootstrap yourself"} {
						if !strings.Contains(d, want) {
							t.Errorf("AgentDirective is missing %q; got %q", want, d)
						}
					}
				}
				if strings.Contains(d, "until an automatic fast-forward lands") {
					t.Errorf("AgentDirective still says the automatic fast-forward has not landed; got %q", d)
				}

				switch arm.decision {
				case "":
					if !strings.Contains(opt.When, "another machine has already migrated and pushed") {
						t.Errorf("the blunt stop has not proven another clone migrated, so its adopt option must stay conditional; got %q", opt.When)
					}
				case gateDecisionAdoptFastForward:
					body := strings.Join(strings.Fields(e.userBody()), " ")
					for _, want := range []string{"no local commit would be lost", "did not run on this open", "drops clone-local data such as wisps"} {
						if !strings.Contains(body, want) {
							t.Errorf("adopt-ff body is missing %q:\n%s", want, body)
						}
					}
					if strings.Contains(body, "nothing local would be lost by adopting") {
						t.Errorf("adopt-ff body still says adopting by re-clone loses nothing:\n%s", body)
					}
				case gateDecisionForkSkew:
					if !strings.Contains(flat, "Pick ONE clone as canonical") || !strings.Contains(opt.When, "choose ONE clone as canonical") {
						t.Errorf("fork-skew must keep the canonical-clone decision; body:\n%s\nwhen: %q", msg, opt.When)
					}
				}
			})
		}
	}
}
