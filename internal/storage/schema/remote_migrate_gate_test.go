package schema

import (
	"context"
	"errors"
	"regexp"
	"slices"
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

// adoptRecoveryCases are the four refusals whose guidance tells this clone to
// adopt the remote's database (bd-bt60zz, #6687): the three smart decisions
// and the blunt fallback stop, whose adopt bullet sits beside the
// designated-migrator one.
func adoptRecoveryCases() []struct {
	name string
	err  *RemoteMigrateGateError
} {
	return []struct {
		name string
		err  *RemoteMigrateGateError
	}{
		{"adopt", &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdopt}},
		{"adopt-ff", &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionAdoptFastForward}},
		{"fork-skew", &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, Decision: gateDecisionForkSkew, SkewVersions: []int{60}}},
		{"blunt", &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, FallbackReason: fallbackReasonOptedOut}},
	}
}

// TestRemoteMigrateGateAdoptRecoveryGuidance pins the embedded adopt recipe in
// every adopting refusal's user message (bd-bt60zz, #6687). `bd bootstrap`
// never replaces an existing database — on this clone it prints "Nothing to
// do." and leaves it on the old schema — so the message has to say so and move
// the database aside, in order, before it names bootstrap.
func TestRemoteMigrateGateAdoptRecoveryGuidance(t *testing.T) {
	for _, tc := range adoptRecoveryCases() {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.UserMessage()
			for _, want := range []string{
				"Nothing to do.",
				"never replaces an existing",
				"unused",
				"Keep the moved-aside copy",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message is missing %q:\n%s", want, msg)
				}
			}
			for _, banned := range []string{"Re-cloning replaces", "replaces your local database", "re-clones and replaces"} {
				if strings.Contains(msg, banned) {
					t.Errorf("message still claims bootstrap replaces the database (%q):\n%s", banned, msg)
				}
			}
			mkdir := strings.Index(msg, "        mkdir -p .beads/backup\n")
			mv := strings.Index(msg, "        mv -f .beads/embeddeddolt .beads/backup/embeddeddolt.pre-adopt\n")
			if mkdir < 0 || mv < 0 || mkdir > mv {
				t.Fatalf("message lacks the ordered mkdir/move recipe (mkdir@%d, mv@%d):\n%s", mkdir, mv, msg)
			}
			if !strings.Contains(msg[mv:], "        bd bootstrap\n") {
				t.Errorf("bd bootstrap does not follow the move:\n%s", msg)
			}
			if strings.Contains(msg[:mkdir], "        bd bootstrap\n") {
				t.Errorf("a bare bd bootstrap command precedes the move:\n%s", msg)
			}
			switch tc.name {
			case "adopt", "blunt":
				// A plain adopt can lose unpushed work: say where it survives
				// and how an old-release export comes back, conditionally.
				for _, want := range []string{"bd import <backup-file>", "OLD bd"} {
					if !strings.Contains(msg, want) {
						t.Errorf("message is missing the preservation note %q:\n%s", want, msg)
					}
				}
			case "adopt-ff":
				// Only the Dolt-history fast-forward is loss-free; adopting by
				// re-clone still omits clone-local data, kept in the aside.
				// Check the body: the Error() heading says "losslessly" on its
				// own and would mask a wrong body.
				body := strings.Join(strings.Fields(tc.err.userBody()), " ")
				for _, want := range []string{"no local commit would be lost", "did not run on this open", "clone-local data such as wisps", "moved-aside copy", "bd import <backup-file>", "OLD bd"} {
					if !strings.Contains(body, want) {
						t.Errorf("adopt-ff body is missing %q:\n%s", want, body)
					}
				}
				for _, banned := range []string{"nothing local would be lost by adopting", "adopt below is loss-free"} {
					if strings.Contains(body, banned) {
						t.Errorf("adopt-ff body still says adopting by re-clone loses nothing (%q):\n%s", banned, body)
					}
				}
			case "fork-skew":
				if !strings.Contains(msg, "canonical") || !strings.Contains(msg, "every OTHER clone") {
					t.Errorf("fork-skew message must keep the choose-a-canonical-clone condition:\n%s", msg)
				}
				for _, want := range []string{"bd import <backup-file>", "OLD bd"} {
					if !strings.Contains(msg, want) {
						t.Errorf("fork-skew message is missing the preservation note %q:\n%s", want, msg)
					}
				}
			}
		})
	}
}

// bd-bt60zz (#6687): on a clone that already has a local database, a bare
// `bd bootstrap` prints "Nothing to do." and leaves the clone on the old
// schema, so the gate must not hand that clone bare bootstrap as the adopt
// step while promising it replaces the local database.
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

// adoptOptionIDs are the Options() ids whose commands adopt the remote.
var adoptOptionIDs = []string{"adopt", "adopt-fast-forward", "reconcile-fork"}

// TestRemoteMigrateGateAdoptOptionsRecipe pins the machine-readable adopt
// paths (bd-bt60zz, #6687): an embedded clone gets the ordered move-aside
// recipe as its commands; server storage, whose database is not a directory in
// this workspace, gets bootstrap gated on explicit host/stop/move/restart
// prerequisites instead of an embedded move. No layout gets an unconditional
// new-binary export or a claim that bootstrap itself replaces data.
func TestRemoteMigrateGateAdoptOptionsRecipe(t *testing.T) {
	layouts := []struct {
		name   string
		set    func(*RemoteMigrateGateError)
		server bool
	}{
		{"embedded", func(*RemoteMigrateGateError) {}, false},
		{"shared", func(e *RemoteMigrateGateError) { e.Shared = true }, true},
		{"proxied", func(e *RemoteMigrateGateError) { e.Shared, e.Proxied = true, true }, true},
	}
	for _, tc := range adoptRecoveryCases() {
		for _, layout := range layouts {
			t.Run(tc.name+"/"+layout.name, func(t *testing.T) {
				e := *tc.err
				layout.set(&e)
				found := 0
				for _, opt := range e.Options() {
					for _, c := range opt.Commands {
						if strings.Contains(c, "bd export") {
							t.Errorf("option %q prescribes a new-binary export %q (it can fail while migrations are pending)", opt.ID, c)
						}
					}
					if !slices.Contains(adoptOptionIDs, opt.ID) {
						continue
					}
					found++
					for _, banned := range []string{"replaces the local database", "re-clones and replaces"} {
						if strings.Contains(opt.Risk, banned) || strings.Contains(opt.When, banned) {
							t.Errorf("option %q still claims bootstrap replaces data: when %q risk %q", opt.ID, opt.When, opt.Risk)
						}
					}
					if !strings.Contains(opt.Risk, "never replaces an existing database") {
						t.Errorf("option %q risk does not explain the bootstrap no-op: %q", opt.ID, opt.Risk)
					}
					if opt.ID == "adopt-fast-forward" {
						// Only the fast-forward is loss-free: the re-clone still
						// omits clone-local data, which the risk must name.
						if strings.HasPrefix(opt.Risk, "none") || !strings.Contains(opt.Risk, "clone-local data") || strings.Contains(opt.Risk, "nothing local is discarded") {
							t.Errorf("adopt-fast-forward risk must name the clone-local data a re-clone omits: %q", opt.Risk)
						}
						if strings.Contains(opt.When, "losslessly") {
							t.Errorf("adopt-fast-forward when still calls the adopt lossless: %q", opt.When)
						}
					}
					if !layout.server {
						if !slices.Equal(opt.Commands, EmbeddedAdoptCommands()) {
							t.Errorf("embedded option %q commands = %q, want the ordered recipe %q", opt.ID, opt.Commands, EmbeddedAdoptCommands())
						}
						if !strings.Contains(opt.When, "unused") {
							t.Errorf("embedded option %q when does not require an unused destination: %q", opt.ID, opt.When)
						}
						continue
					}
					if !slices.Equal(opt.Commands, []string{"bd bootstrap"}) {
						t.Errorf("server option %q commands = %q, want only bd bootstrap behind its prerequisites", opt.ID, opt.Commands)
					}
					for _, want := range []string{"host", "stopped", "data_dir", "restarted", "downtime"} {
						if !strings.Contains(opt.When, want) {
							t.Errorf("server option %q when is missing prerequisite %q: %q", opt.ID, want, opt.When)
						}
					}
					if strings.Contains(opt.When, ".beads/embeddeddolt") {
						t.Errorf("server option %q prescribes an embedded move: %q", opt.ID, opt.When)
					}
				}
				if found != 1 {
					t.Errorf("found %d adopt options, want exactly 1", found)
				}
			})
		}
	}
}

// TestRemoteMigrateGateServerAdoptGuidance pins the server-storage adopt body
// (bd-bt60zz, #6687) for an owned server (ServerMode, set by the Dolt store),
// a shared one, and a proxied client: the database is not a directory in this
// workspace, so the message names server storage and the serving host's
// data_dir, and never prescribes the embedded move or a fixed shared path.
func TestRemoteMigrateGateServerAdoptGuidance(t *testing.T) {
	layouts := []struct {
		name string
		set  func(*RemoteMigrateGateError)
	}{
		{"owned", func(e *RemoteMigrateGateError) { e.ServerMode = true }},
		{"shared", func(e *RemoteMigrateGateError) { e.ServerMode, e.Shared = true, true }},
		{"proxied", func(e *RemoteMigrateGateError) { e.Shared, e.Proxied = true, true }},
	}
	for _, tc := range adoptRecoveryCases() {
		for _, layout := range layouts {
			t.Run(tc.name+"/"+layout.name, func(t *testing.T) {
				e := *tc.err
				layout.set(&e)
				msg := e.UserMessage()
				for _, want := range []string{"server storage", "data_dir", "stop the", "Restart the server", "re-clone gotchas", "never replaces an existing"} {
					if !strings.Contains(msg, want) {
						t.Errorf("server adopt message is missing %q:\n%s", want, msg)
					}
				}
				for _, banned := range []string{".beads/embeddeddolt", "mkdir -p .beads/backup"} {
					if strings.Contains(msg, banned) {
						t.Errorf("server adopt message prescribes the embedded move (%q):\n%s", banned, msg)
					}
				}
				// .beads/dolt/ is only the default of a server bd runs for this
				// workspace; it must never be stated as where a server keeps it.
				if strings.Contains(msg, ".beads/dolt/") && !strings.Contains(msg, ".beads/dolt/ only for a server bd runs for this workspace") {
					t.Errorf("server adopt message names .beads/dolt/ as a fixed location:\n%s", msg)
				}
				idx := strings.Index(msg, "data_dir")
				if idx < 0 || !strings.Contains(msg[idx:], "        bd bootstrap\n") {
					t.Errorf("bd bootstrap must follow the data_dir move:\n%s", msg)
				}
				if !slices.Equal(e.Options()[len(e.Options())-1].Commands, []string{"bd bootstrap"}) {
					t.Errorf("server adopt option commands = %q, want bd bootstrap behind prerequisites", e.Options()[len(e.Options())-1].Commands)
				}
			})
		}
	}
}

// TestRemoteMigrateGateAdoptAgentDirective pins the agent-facing directive of
// the three adopting decisions (bd-bt60zz, #6687): it must say bootstrap alone
// changes nothing on an existing database and the database is moved aside
// first, keep the surface-to-the-operator / do-not-auto-run contract, and no
// longer claim the automatic fast-forward has not landed (it has: the smart
// gate runs it before ever returning adopt-ff).
func TestRemoteMigrateGateAdoptAgentDirective(t *testing.T) {
	for _, tc := range adoptRecoveryCases() {
		if tc.name == "blunt" {
			continue
		}
		for _, server := range []bool{false, true} {
			name := tc.name + "/embedded"
			if server {
				name = tc.name + "/server"
			}
			t.Run(name, func(t *testing.T) {
				e := *tc.err
				e.ServerMode = server
				d := e.AgentDirective()
				for _, want := range []string{"never replaces an existing database", "moved aside first", "remote_migrate_gate.options", "operator", "do NOT auto-run"} {
					if !strings.Contains(d, want) {
						t.Errorf("directive is missing %q: %s", want, d)
					}
				}
				for _, banned := range []string{"until an automatic fast-forward lands", "re-clones and replaces", "bd migrate --force"} {
					if strings.Contains(d, banned) {
						t.Errorf("directive still says %q: %s", banned, d)
					}
				}
				if server && !strings.Contains(d, "serving host") {
					t.Errorf("server directive does not place the move on the serving host: %s", d)
				}
				if !server && strings.Contains(d, "serving host") {
					t.Errorf("embedded directive talks about a serving host: %s", d)
				}
			})
		}
	}
}

// TestRemoteMigrateGateNonAdoptGuidanceUnchanged guards the refusals the
// adopt-recipe change (bd-bt60zz, #6687) must not touch: the data-behind stop
// keeps its single pull remedy (plus the shared consent step), shared-no-remote
// keeps its verb consent, and the blunt stop keeps its designated-migrator
// branch, option ids, escape hint and closing note.
func TestRemoteMigrateGateNonAdoptGuidanceUnchanged(t *testing.T) {
	ids := func(opts []GateOption) []string {
		var out []string
		for _, o := range opts {
			out = append(out, o.ID)
		}
		return out
	}
	noAdoptRecipe := func(t *testing.T, e *RemoteMigrateGateError) {
		t.Helper()
		all := e.UserMessage() + e.AgentDirective()
		for _, o := range e.Options() {
			all += o.When + o.Risk + strings.Join(o.Commands, "\n")
		}
		for _, banned := range []string{"embeddeddolt.pre-adopt", "mkdir -p .beads/backup", "data_dir"} {
			if strings.Contains(all, banned) {
				t.Errorf("non-adopt refusal gained adopt-recipe text %q:\n%s", banned, all)
			}
		}
	}

	t.Run("data-behind", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			shared, proxied  bool
			wantIDs          []string
			wantFirstCommand string
		}{
			{"plain", false, false, []string{"pull-first"}, DataBehindRemedyCommand},
			{"shared", true, false, []string{"pull-first", "migrate-shared-after-pulling"}, DataBehindRemedyCommand},
			{"proxied", true, true, []string{"pull-first-on-server-host", "migrate-shared-after-pulling"}, DataBehindRemedyCommand},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := &RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1,
					FallbackReason: fallbackReasonDataBehind, Shared: tc.shared, Proxied: tc.proxied, ServerMode: tc.shared && !tc.proxied}
				if !e.IsDataBehind() {
					t.Fatal("fixture is not the data-behind stop")
				}
				opts := e.Options()
				if got := ids(opts); !slices.Equal(got, tc.wantIDs) {
					t.Errorf("option ids = %q, want %q", got, tc.wantIDs)
				}
				if !slices.Equal(opts[0].Commands, []string{tc.wantFirstCommand}) {
					t.Errorf("pull option commands = %q", opts[0].Commands)
				}
				if tc.shared && !slices.Equal(opts[1].Commands, []string{SharedConsentCommandForced}) {
					t.Errorf("consent option commands = %q, want %q", opts[1].Commands, SharedConsentCommandForced)
				}
				if e.EscapeHint() != "bd migrate --force" {
					t.Errorf("EscapeHint = %q", e.EscapeHint())
				}
				if !strings.Contains(e.UserMessage(), "Pull first — that is the whole remedy:") {
					t.Errorf("data-behind body lost its pull-first remedy:\n%s", e.UserMessage())
				}
				noAdoptRecipe(t, e)
			})
		}
	})

	t.Run("shared-no-remote", func(t *testing.T) {
		e := &RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 67, Pending: 1, Decision: gateDecisionSharedNoRemote, Shared: true, ServerMode: true}
		opts := e.Options()
		if got := ids(opts); !slices.Equal(got, []string{"migrate-shared"}) {
			t.Errorf("option ids = %q", got)
		}
		if !slices.Equal(opts[0].Commands, []string{SharedConsentCommand}) {
			t.Errorf("commands = %q", opts[0].Commands)
		}
		if e.EscapeHint() != SharedConsentCommand {
			t.Errorf("EscapeHint = %q, want %q", e.EscapeHint(), SharedConsentCommand)
		}
		if !strings.Contains(e.AgentDirective(), "do NOT auto-run the migration") {
			t.Errorf("directive lost its consent requirement: %s", e.AgentDirective())
		}
		noAdoptRecipe(t, e)
	})

	t.Run("blunt designated migrator", func(t *testing.T) {
		e := &RemoteMigrateGateError{CurrentVersion: 53, LatestVersion: 67, Pending: 14, FallbackReason: fallbackReasonOptedOut}
		opts := e.Options()
		if got := ids(opts); !slices.Equal(got, []string{"migrate", "adopt"}) {
			t.Errorf("option ids = %q", got)
		}
		if !slices.Equal(opts[0].Commands, []string{"bd migrate --force", "bd dolt push"}) {
			t.Errorf("migrate commands = %q", opts[0].Commands)
		}
		if opts[0].When != "you are the single designated migrator (only ONE machine, confirmed with the operator) and no other clone has migrated yet" {
			t.Errorf("migrate precondition changed: %q", opts[0].When)
		}
		if e.EscapeHint() != "bd migrate --force" {
			t.Errorf("EscapeHint = %q", e.EscapeHint())
		}
		if e.AgentDirective() != "Coordination decision required: only ONE clone may migrate a shared remote; "+
			"a second clone migrating independently forks the schema unrecoverably (#4259). "+
			"Do NOT auto-run a migration — surface remote_migrate_gate.options to the operator and let them choose." {
			t.Errorf("blunt directive changed: %s", e.AgentDirective())
		}
		msg := e.UserMessage()
		for _, want := range []string{
			"    • You are the designated migrator (only ONE machine should be): migrate,\n" +
				"      then publish the migrated database to the remote:\n" +
				"        bd migrate --force\n" +
				"        (or " + AllowRemoteMigrateEnv + "=1 bd migrate in scripted/CI use)\n" +
				"        bd dolt push\n",
			"  Several machines on one remote? Only ONE migrates. Get every other clone\n",
			"  Smart gate (#4516): it is disabled (" + SmartGateEnv + "=0).\n",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("blunt message lost %q:\n%s", want, msg)
			}
		}
	})
}
