package schema

import (
	"context"
	"errors"
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
