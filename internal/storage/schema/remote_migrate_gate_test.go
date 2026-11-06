package schema

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

// TestRemoteMigrateGateAdoptRecoveryGuidance pins the #6687 fix at the body
// layer: every embedded adoption path — adopt, adopt-ff, fork-skew, and the
// blunt arm's adopt bullet — must name the existing-database no-op, present
// directory creation and the move-aside BEFORE bootstrap, and carry its
// preservation conditions, instead of prescribing a bare `bd bootstrap`
// that reports "Nothing to do." and loops the refusal.
func TestRemoteMigrateGateAdoptRecoveryGuidance(t *testing.T) {
	cases := []struct {
		name string
		e    *RemoteMigrateGateError
		// canonicalWant: the arm must name its extra precondition.
		canonicalWant string
	}{
		{name: "adopt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}},
		{name: "adopt-ff", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdoptFastForward}},
		{name: "fork-skew", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionForkSkew, SkewVersions: []int{54}}, canonicalWant: "canonical"},
		{name: "blunt adopt bullet", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.e.UserMessage()

			// The existing-database no-op is explained, not just implied.
			if !strings.Contains(msg, "Nothing to do.") {
				t.Errorf("adoption path does not explain the existing-database no-op:\n%s", msg)
			}
			// The recipe is present and ordered: create the destination
			// parent, move the database aside, THEN bootstrap.
			mkdirAt := strings.Index(msg, "mkdir -p .beads/backup")
			mvAt := strings.Index(msg, "mv -f .beads/embeddeddolt .beads/backup/embeddeddolt.pre-adopt")
			if mkdirAt < 0 || mvAt < 0 {
				t.Fatalf("adoption path lacks the move-aside recipe (mkdir at %d, mv at %d):\n%s", mkdirAt, mvAt, msg)
			}
			if mvAt < mkdirAt {
				t.Errorf("destination creation must precede the move-aside:\n%s", msg)
			}
			if next := strings.Index(msg[mvAt:], "bd bootstrap"); next < 0 {
				t.Errorf("no bootstrap step after the move-aside:\n%s", msg)
			}
			// Preservation: the aside is kept, never deleted, and holds the
			// local work a re-clone cannot bring back.
			if !strings.Contains(msg, "never delete") {
				t.Errorf("adoption path does not say to preserve the aside:\n%s", msg)
			}
			// Conditions: unused destination and substitute-configured-path
			// notes travel with the recipe.
			if !strings.Contains(msg, "dolt_data_dir") || !strings.Contains(msg, "already exists") {
				t.Errorf("recipe conditions (configured path, unused destination) missing:\n%s", msg)
			}
			if tc.canonicalWant != "" && !strings.Contains(msg, tc.canonicalWant) {
				t.Errorf("adoption path missing its %q condition:\n%s", tc.canonicalWant, msg)
			}
			// The false claims are gone: bootstrap is never described as
			// replacing the database, and gated push/export are not required
			// as recovery prerequisites.
			for _, stale := range []string{
				"Re-cloning replaces",
				"replaces your local database",
				"Push first (`bd dolt push`) or save a copy",
			} {
				if strings.Contains(msg, stale) {
					t.Errorf("adoption path still carries the stale claim %q:\n%s", stale, msg)
				}
			}
		})
	}
}

// TestRemoteMigrateGateAdoptGuidanceIsNotBareBootstrap is the #6687 repro:
// on a clone that already has a local database, a bare `bd bootstrap` prints
// "Nothing to do." and leaves the clone on the old schema, so the gate must
// not hand that clone bare bootstrap as the adopt step while promising it
// replaces the local database.
func TestRemoteMigrateGateAdoptGuidanceIsNotBareBootstrap(t *testing.T) {
	bareThenReplace := regexp.MustCompile(`(?m)^\s*bd bootstrap\n.*replaces your local database`)
	for _, decision := range []string{"", gateDecisionAdopt} {
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: decision}
		if msg := e.UserMessage(); bareThenReplace.MatchString(msg) {
			t.Errorf("decision %q: refusal tells an existing clone that bare `bd bootstrap` replaces its database:\n%s", decision, msg)
		}
		for _, opt := range e.Options() {
			if opt.ID == "adopt" && len(opt.Commands) == 1 && opt.Commands[0] == "bd bootstrap" {
				t.Errorf("decision %q: adopt option is bare `bd bootstrap` (a no-op on an existing database), risk %q", decision, opt.Risk)
			}
		}
	}
}

// TestRemoteMigrateGateAdoptOptionsCarryRecovery pins the #6687 fix at the
// options layer. An option whose whole command list is `bd bootstrap` is a
// dead end on the very clone this refusal came from — the database that made
// the gate fire is still there, and bootstrap refuses to replace it — so the
// embedded adopt paths must carry the ordered move-aside recipe (directory
// creation, move, bootstrap), must not demand an unconditional export, and
// must not sell the adopt as "replaces the local database". Server storage
// (shared #5920, proxied) cannot express its recovery as commands run from
// HERE at all: the move happens on the serving host, out of the server's
// actual data_dir, with the server stopped — so those options carry the
// prerequisites in When and keep the bootstrap alone, conditional behind
// them, instead of handing an agent an embedded `mv` that cannot work there.
func TestRemoteMigrateGateAdoptOptionsCarryRecovery(t *testing.T) {
	findOption := func(t *testing.T, opts []GateOption, id string) GateOption {
		t.Helper()
		for _, opt := range opts {
			if opt.ID == id {
				return opt
			}
		}
		t.Fatalf("no %q option among %v", id, opts)
		return GateOption{}
	}

	t.Run("embedded carries the ordered move-aside recipe", func(t *testing.T) {
		cases := []struct {
			name     string
			e        *RemoteMigrateGateError
			optionID string
		}{
			{name: "blunt adopt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14}, optionID: "adopt"},
			{name: "adopt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}, optionID: "adopt"},
			{name: "adopt-ff", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdoptFastForward}, optionID: "adopt-fast-forward"},
			{name: "fork-skew", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionForkSkew, SkewVersions: []int{54}}, optionID: "reconcile-fork"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				opt := findOption(t, tc.e.Options(), tc.optionID)

				mkdirAt := -1
				mvAt := -1
				bootAt := -1
				for i, cmd := range opt.Commands {
					switch {
					case cmd == "mkdir -p .beads/backup":
						mkdirAt = i
					case cmd == "mv -f .beads/embeddeddolt .beads/backup/embeddeddolt.pre-adopt":
						mvAt = i
					case cmd == "bd bootstrap":
						bootAt = i
					case strings.HasPrefix(cmd, "bd export"):
						t.Errorf("option demands an unconditional export command %q; exporting with the NEW binary is conditional guidance, not a step (#6687)", cmd)
					}
				}
				if mkdirAt < 0 || mvAt < 0 || bootAt < 0 {
					t.Fatalf("option commands are not the move-aside recipe (mkdir %d, mv %d, bootstrap %d): %v", mkdirAt, mvAt, bootAt, opt.Commands)
				}
				if !(mkdirAt < mvAt && mvAt < bootAt) {
					t.Errorf("move-aside recipe is out of order: %v", opt.Commands)
				}
				// The When must name why the recipe exists: bootstrap alone
				// no-ops against the existing database.
				if !strings.Contains(opt.When, "Nothing to do.") {
					t.Errorf("option When does not name the existing-database no-op:\n%s", opt.When)
				}
				// And its conditions travel with it.
				if !strings.Contains(opt.When, "dolt_data_dir") || !strings.Contains(opt.When, "nothing occupies") {
					t.Errorf("option When lacks the recipe conditions (configured path, unused destination):\n%s", opt.When)
				}
				if strings.Contains(opt.Risk, "replaces the local database") {
					t.Errorf("option Risk still claims bootstrap replaces the local database:\n%s", opt.Risk)
				}
			})
		}
	})

	t.Run("server storage carries host prerequisites, not embedded moves", func(t *testing.T) {
		base := func(mod func(*RemoteMigrateGateError)) *RemoteMigrateGateError {
			e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}
			mod(e)
			return e
		}
		cases := []struct {
			name     string
			e        *RemoteMigrateGateError
			optionID string
		}{
			{name: "shared adopt", e: base(func(e *RemoteMigrateGateError) { e.Shared = true }), optionID: "adopt"},
			{name: "proxied adopt", e: base(func(e *RemoteMigrateGateError) { e.Proxied = true }), optionID: "adopt"},
			{name: "shared adopt-ff", e: base(func(e *RemoteMigrateGateError) { e.Shared = true; e.Decision = gateDecisionAdoptFastForward }), optionID: "adopt-fast-forward"},
			{name: "proxied fork-skew", e: base(func(e *RemoteMigrateGateError) { e.Proxied = true; e.Decision = gateDecisionForkSkew; e.SkewVersions = []int{54} }), optionID: "reconcile-fork"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				opt := findOption(t, tc.e.Options(), tc.optionID)

				// The prerequisites are in When: stop, host, move, restart,
				// and the data_dir that only the server's config can name.
				for _, want := range []string{"stop", "host", "move", "restart", "data_dir"} {
					if !strings.Contains(opt.When, want) {
						t.Errorf("option When lacks the server prerequisite %q:\n%s", want, opt.When)
					}
				}
				// The bootstrap is the only command, conditional on the
				// prerequisites above — no embedded `mv` can run from here.
				if len(opt.Commands) != 1 || opt.Commands[0] != "bd bootstrap" {
					t.Errorf("server option commands = %v; want just the conditional `bd bootstrap` (the move happens on the host)", opt.Commands)
				}
				// No embedded path, and no fabricated shared-server location:
				// the refusal cannot know the server's data_dir layout.
				for _, unwanted := range []string{"embeddeddolt", "shared-server/dolt"} {
					if strings.Contains(opt.When, unwanted) || strings.Contains(opt.Risk, unwanted) {
						t.Errorf("server option names %q, which is not knowable from this refusal; When:\n%s\nRisk:\n%s", unwanted, opt.When, opt.Risk)
					}
				}
			})
		}
	})
}

// TestRemoteMigrateGateServerModeAdoptionGuidance pins the #6687 fix at the
// storage-mode layer. The gate fires from embedded AND server storage, but
// the move-aside recipe is an embedded move: on a database a dolt sql-server
// serves, the directory has to be moved on the host, out of the server's
// actual data_dir — an `mv -f .beads/embeddeddolt` printed there prescribes a
// command that cannot work. Every adopt path must therefore render the
// coordinated-downtime steps when ANY server marker is set: ServerMode (the
// workspace's own owned server), Shared (#5920), or Proxied.
func TestRemoteMigrateGateServerModeAdoptionGuidance(t *testing.T) {
	modes := []struct {
		name string
		set  func(*RemoteMigrateGateError)
	}{
		{name: "owned server (ServerMode)", set: func(e *RemoteMigrateGateError) { e.ServerMode = true }},
		{name: "shared (Shared)", set: func(e *RemoteMigrateGateError) { e.Shared = true }},
		{name: "proxied (Proxied)", set: func(e *RemoteMigrateGateError) { e.Proxied = true }},
	}
	cases := []struct {
		name     string
		e        *RemoteMigrateGateError
		optionID string
	}{
		{name: "adopt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}},
		{name: "adopt-ff", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdoptFastForward}},
		{name: "fork-skew", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionForkSkew, SkewVersions: []int{54}}},
		{name: "blunt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14}},
	}
	for _, mode := range modes {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				e := *tc.e
				mode.set(&e)
				msg := e.UserMessage()

				// Lowercased scan: the numbered steps capitalize their
				// verbs ("Restart the server"), the option prose does not.
				lowered := strings.ToLower(msg)
				for _, want := range []string{"server", "data_dir", "stop", "restart", "nothing to do."} {
					if !strings.Contains(lowered, want) {
						t.Errorf("server adoption guidance is missing %q:\n%s", want, msg)
					}
				}
				// The embedded recipe must not print on server storage: its
				// source path is meaningless there, and a fabricated shared
				// location is worse (the refusal cannot know the server's
				// data_dir layout).
				for _, unwanted := range []string{"embeddeddolt", "mv -f", "mkdir -p"} {
					if strings.Contains(msg, unwanted) {
						t.Errorf("server adoption guidance carries the embedded move %q:\n%s", unwanted, msg)
					}
				}
			})
		}
	}

	t.Run("embedded stays embedded", func(t *testing.T) {
		// The same decisions with no server marker keep the local recipe —
		// this is what an embedded-mode clone (the default) sees.
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}
		if !strings.Contains(e.UserMessage(), "mv -f .beads/embeddeddolt") {
			t.Errorf("embedded adoption guidance lost its local move-aside recipe:\n%s", e.UserMessage())
		}
	})
}

// TestRemoteMigrateGateAdoptDirectiveNamesMoveAside pins the #6687 fix at the
// agent-directive layer. The directive is what an agent reads INSTEAD of the
// options, so it must carry the same fact the options now do: a bare
// `bd bootstrap` no-ops against the existing database, adoption starts by
// moving it aside, and on server storage the move happens on the host. It
// must also stay what it always was — a non-runnable instruction to surface
// the choice to the operator, including for the loss-free fast-forward case,
// whose directive used to promise an automatic fast-forward "until (#4259
// follow-up) lands" that has since landed without changing this contract.
func TestRemoteMigrateGateAdoptDirectiveNamesMoveAside(t *testing.T) {
	cases := []struct {
		name     string
		e        *RemoteMigrateGateError
		lossFree bool
	}{
		{name: "adopt", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}},
		{name: "adopt-ff", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdoptFastForward}, lossFree: true},
		{name: "fork-skew", e: &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionForkSkew, SkewVersions: []int{54}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.e.AgentDirective()

			// The move-aside prerequisite is named, not implied: an agent
			// told only "adopt" will run the bare bootstrap, which no-ops.
			if !strings.Contains(d, "aside") {
				t.Errorf("directive does not name the move-aside prerequisite:\n%s", d)
			}
			if !strings.Contains(d, "Nothing to do.") {
				t.Errorf("directive does not explain why a bare bootstrap no-ops:\n%s", d)
			}
			// The directive stays a surfaced choice, never an auto-run.
			if !strings.Contains(d, "do NOT auto-run") {
				t.Errorf("directive lost its do-NOT-auto-run instruction:\n%s", d)
			}
			// And stays non-runnable.
			if strings.Contains(d, "bd migrate --force") || strings.Contains(d, AllowRemoteMigrateEnv+"=1 bd migrate") {
				t.Errorf("directive embeds a runnable escape command:\n%s", d)
			}
			if tc.lossFree && strings.Contains(d, "automatic fast-forward lands") {
				t.Errorf("directive still promises an automatic fast-forward that has landed (#4259 follow-up):\n%s", d)
			}
		})

		t.Run(tc.name+"+server", func(t *testing.T) {
			e := *tc.e
			e.ServerMode = true
			d := e.AgentDirective()

			// Server storage: the move is on the host, out of the server's
			// data_dir — the directive must say so rather than let an agent
			// attempt an embedded move.
			if !strings.Contains(d, "server host") || !strings.Contains(d, "data_dir") {
				t.Errorf("server directive lacks the host/data_dir addendum:\n%s", d)
			}
			if strings.Contains(d, "embeddeddolt") {
				t.Errorf("server directive names the embedded path:\n%s", d)
			}
		})
	}
}

// TestRemoteMigrateGateUnchangedRemedies pins the remedies #6687 did NOT
// touch. The fix reshaped only the adopt paths (their bodies, options and
// directives); the data-behind stop, the shared-no-remote consent path and
// the blunt designated-migrator path must keep their exact commands and
// contracts, because each is already correct for its state — a pull for a
// clone behind on data, a consent verb for a shared server with no remote,
// and the single-designated-migrator migrate for the undecided stop. If one
// of these assertions fails, the adopt rework overreached.
func TestRemoteMigrateGateUnchangedRemedies(t *testing.T) {
	t.Run("data-behind keeps its single pull option", func(t *testing.T) {
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 53, Pending: 14, FallbackReason: "data-behind"}
		if !e.IsDataBehind() {
			t.Fatal("fixture does not build the data-behind stop")
		}
		if msg := e.UserMessage(); !strings.Contains(msg, "Pull first — that is the whole remedy") {
			t.Errorf("data-behind body lost its pull-first framing:\n%s", msg)
		}
		opts := e.Options()
		if len(opts) != 1 || opts[0].ID != "pull-first" {
			t.Fatalf("data-behind options = %+v; want the single pull-first option", opts)
		}
		if len(opts[0].Commands) != 1 || opts[0].Commands[0] != DataBehindRemedyCommand {
			t.Errorf("pull-first commands = %v; want [%s]", opts[0].Commands, DataBehindRemedyCommand)
		}
	})

	t.Run("shared-no-remote keeps its consent option", func(t *testing.T) {
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionSharedNoRemote, Shared: true}
		if msg := e.UserMessage(); !strings.Contains(msg, "Read commands keep working") {
			t.Errorf("shared-no-remote body lost its reads-keep-working promise:\n%s", msg)
		}
		opts := e.Options()
		if len(opts) != 1 || opts[0].ID != "migrate-shared" {
			t.Fatalf("shared-no-remote options = %+v; want the single migrate-shared option", opts)
		}
		if len(opts[0].Commands) != 1 || opts[0].Commands[0] != SharedConsentCommand {
			t.Errorf("migrate-shared commands = %v; want [%s]", opts[0].Commands, SharedConsentCommand)
		}
		if e.EscapeHint() != SharedConsentCommand {
			t.Errorf("shared-no-remote escape hint = %q; want %s", e.EscapeHint(), SharedConsentCommand)
		}
	})

	t.Run("blunt keeps the designated-migrator path", func(t *testing.T) {
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14}

		for _, want := range []string{
			"You are the designated migrator (only ONE machine should be)",
			"Several machines on one remote? Only ONE migrates",
		} {
			if msg := e.UserMessage(); !strings.Contains(msg, want) {
				t.Errorf("blunt body lost %q:\n%s", want, msg)
			}
		}
		if e.EscapeHint() != "bd migrate --force" {
			t.Errorf("blunt escape hint = %q; want bd migrate --force", e.EscapeHint())
		}
		opts := e.Options()
		if len(opts) != 2 || opts[0].ID != "migrate" || opts[1].ID != "adopt" {
			t.Fatalf("blunt options = %+v; want migrate then adopt", opts)
		}
		wantMigrate := []string{"bd migrate --force", "bd dolt push"}
		if len(opts[0].Commands) != 2 || opts[0].Commands[0] != wantMigrate[0] || opts[0].Commands[1] != wantMigrate[1] {
			t.Errorf("migrate option commands = %v; want %v", opts[0].Commands, wantMigrate)
		}
		if !strings.Contains(opts[0].When, "single designated migrator") {
			t.Errorf("migrate option lost its single-migrator precondition:\n%s", opts[0].When)
		}
	})
}

// TestUpgradingGuideAdoptRecipeMatchesGate pins the #6687 alignment between
// the gate's adopt recipe and the upgrade guide's copy. The guide is the doc
// an operator follows by hand while the gate is the thing refusing their
// commands; if they disagree — the guide's move misses the destination parent
// the gate creates, names a different destination, or presents the post-clone
// import as unconditional when the gate calls it conditional — the operator
// is back to a recipe that fails or loops. The guide is read as test data,
// like the divergence-ledger doc precedent.
func TestUpgradingGuideAdoptRecipeMatchesGate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "getting-started", "upgrading.md"))
	if err != nil {
		t.Fatalf("read upgrading.md (wired as test data; see the package BUILD): %v", err)
	}
	guide := string(raw)

	// The same move, to the same destination, with the parent created first —
	// in order, as in AdoptMoveAsideCommands.
	mkdirAt := strings.Index(guide, "mkdir -p "+adoptAsideParent)
	mvAt := strings.Index(guide, "mv .beads/embeddeddolt "+adoptAsideDest)
	if mkdirAt < 0 || mvAt < 0 {
		t.Fatalf("guide's adopt recipe does not match the gate's paths (mkdir at %d, mv at %d):\n%s", mkdirAt, mvAt, guide)
	}
	if mvAt < mkdirAt {
		t.Errorf("guide moves the database before creating the destination parent; the gate creates it first (AdoptMoveAsideCommands)")
	}
	// The recipe's conditions travel with it: an unused destination, and the
	// preservation of the aside as the recovery copy.
	if !strings.Contains(guide, "unused") || !strings.Contains(guide, "another name") {
		t.Errorf("guide does not demand an unused destination for the move-aside")
	}
	if !strings.Contains(guide, "Keep the moved-aside") {
		t.Errorf("guide dropped the preservation note for the moved-aside directory")
	}
	// The post-clone import is conditional, the same way the gate treats
	// export: only sound for work the old binary produced.
	importAt := strings.Index(guide, "bd import .beads/backup/pre-migrate.jsonl")
	if importAt < 0 {
		t.Fatalf("guide lost the post-clone import step:\n%s", guide)
	}
	if !strings.Contains(guide[importAt:], "only if") && !strings.Contains(guide[:importAt], "only if") {
		t.Errorf("guide presents the import as unconditional; the backup can only come from the old binary")
	}
	if !strings.Contains(guide, "skips the import") {
		t.Errorf("guide does not say a clone without an old-binary backup skips the import")
	}
}
