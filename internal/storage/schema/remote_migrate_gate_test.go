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
			bootstrapAt := mv + strings.Index(msg[mv:], "        bd bootstrap\n")
			assertAdoptRemoteChecks(t, msg, mkdir, bootstrapAt)
			assertAdoptMoveWindowChecks(t, msg, mkdir, bootstrapAt)
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

// assertAdoptRemoteChecks pins the remote half of an adopt recipe (#6687
// review): bootstrap finds the remote only through sync.remote or git origin,
// never through the database the recipe moves aside, so msg must check
// sync.remote before the move (at moveAt) and accept only a clone plan after
// the bootstrap command (at bootstrapAt). The remedy for a wrong sync.remote
// is an edit of config.yaml: this gate refuses `bd config set` on the same
// database, so a recipe that prescribes it loops on the refusal.
func assertAdoptRemoteChecks(t *testing.T, msg string, moveAt, bootstrapAt int) {
	t.Helper()
	if moveAt < 0 || bootstrapAt < moveAt {
		t.Fatalf("message lacks the move (@%d) or the bootstrap after it (@%d):\n%s", moveAt, bootstrapAt, msg)
	}
	before := strings.Join(strings.Fields(msg[:moveAt]), " ")
	for _, want := range []string{
		"`bd config get sync.remote` prints the remote to adopt",
		"set `remote: <url>` under `sync:` in the config.yaml of",
		"by hand",
		"`bd config set` is refused while this database has pending migrations",
		"clones from sync.remote (or, if that is unset, from Dolt data on git origin)",
		"the database you move",
	} {
		if !strings.Contains(before, want) {
			t.Errorf("the remote check %q is missing or comes after the move:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "bd config set sync.remote") {
		t.Errorf("the recipe prescribes `bd config set sync.remote`, which this gate refuses:\n%s", msg)
	}
	after := strings.Join(strings.Fields(msg[bootstrapAt:]), " ")
	for _, want := range []string{
		"its plan is \"clone from remote\" for that remote",
		// Bootstrap prints a git-forge remote in its routed git+ form with
		// the credentials stripped, never as `bd config get sync.remote`
		// prints it, so a literal comparison would decline the right plan.
		"it shows a git URL in its git+https:// or git+ssh:// form, without credentials",
		// It skips the prompt under --yes, BD_NON_INTERACTIVE or CI.
		"It asks first only in a terminal without --yes, BD_NON_INTERACTIVE or CI set",
		"`bd bootstrap --dry-run`",
		"did not find the remote",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the plan check %q does not follow bd bootstrap:\n%s", want, msg)
		}
	}
	if strings.Contains(after, "from that URL") {
		t.Errorf("the plan check still compares against the configured URL literally:\n%s", msg)
	}
}

// assertAdoptMoveWindowChecks pins the embedded recipe's guard on the window
// between the move (at moveAt) and the clone (#6687 review): with no database
// in place, `bd prime` (agent session hooks run it) creates a fresh one, and
// in another git worktree that worktree then keeps its own. So msg must name
// those processes among the ones to stop and anchor the commands to `bd where`
// before the move, and after bootstrap (at bootstrapAt) say what a report of
// an existing database means and check each worktree with `bd where`.
func assertAdoptMoveWindowChecks(t *testing.T, msg string, moveAt, bootstrapAt int) {
	t.Helper()
	before := strings.Join(strings.Fields(msg[:moveAt]), " ")
	for _, want := range []string{
		"stop every bd process that uses this database, and start none until bootstrap has finished",
		"agent sessions whose hooks run `bd prime`",
		"bd in this repository's other git worktrees",
		"`bd prime` creates a fresh database where it finds none",
		"from the directory that contains the .beads that `bd where` reports",
	} {
		if !strings.Contains(before, want) {
			t.Errorf("the move-window guard %q is missing or comes after the move:\n%s", want, msg)
		}
	}
	after := strings.Join(strings.Fields(msg[bootstrapAt:]), " ")
	for _, want := range []string{
		"If it reports instead that the database already exists, a bd process created a new one after the move",
		"move that one aside too",
		"run `bd where` in each of the repository's other git worktrees: each must report this .beads",
		"A worktree that reports its own .beads instead gained a database during the move: move that worktree's .beads/embeddeddolt aside",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the post-move check %q does not follow bd bootstrap:\n%s", want, msg)
		}
	}
	// The embedded database is always the active .beads's embeddeddolt/, so
	// there is no other database path to move.
	if strings.Contains(msg, "configures its own database path") {
		t.Errorf("the recipe still points at a configurable embedded database path:\n%s", msg)
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
					// #6687 review: bootstrap cannot read the remote from the
					// moved database, so every adopt path checks sync.remote
					// first (fixed in config.yaml: the gate refuses bd config
					// set) and accepts only a clone plan; and the keep note
					// matches the text's "until checked" (no "never delete").
					for _, want := range []string{
						"bd config get sync.remote prints the remote to adopt",
						"else first set remote: <url> under sync: in the workspace's .beads/config.yaml by hand",
						"bd config set is refused while this database has pending migrations",
						"never from the database moved aside",
					} {
						if !strings.Contains(opt.When, want) {
							t.Errorf("option %q when is missing the remote check %q: %q", opt.ID, want, opt.When)
						}
					}
					if strings.Contains(opt.When, "bd config set sync.remote") || strings.Contains(opt.Risk, "bd config set sync.remote") {
						t.Errorf("option %q prescribes bd config set sync.remote, which the gate refuses: when %q risk %q", opt.ID, opt.When, opt.Risk)
					}
					for _, want := range []string{
						"its plan is clone from remote for that remote",
						"it shows a git URL in its git+https:// or git+ssh:// form, without credentials",
						"it asks first only in a terminal without --yes, BD_NON_INTERACTIVE or CI set",
						"bd bootstrap --dry-run",
						"until you have checked the result",
					} {
						if !strings.Contains(opt.Risk, want) {
							t.Errorf("option %q risk is missing %q: %q", opt.ID, want, opt.Risk)
						}
					}
					if strings.Contains(opt.Risk, "from that URL") {
						t.Errorf("option %q risk still compares against the configured URL literally: %q", opt.ID, opt.Risk)
					}
					if strings.Contains(opt.Risk, "never delete") {
						t.Errorf("option %q risk still says never delete the moved copy: %q", opt.ID, opt.Risk)
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
						if !strings.Contains(opt.Risk, "backup.size-cap-mb") {
							t.Errorf("embedded option %q risk does not say the copy under .beads/backup counts toward the backup cap: %q", opt.ID, opt.Risk)
						}
						// #6687 review: the move window. A bd that opens lazily
						// (bd prime, run by agent hooks) creates a fresh
						// database while the old one is moved, in this
						// workspace or in another git worktree's own .beads.
						for _, want := range []string{
							"run from the directory that contains the .beads that bd where reports",
							"every bd process that uses this database stopped and none started until bootstrap has finished",
							"agent sessions whose hooks run bd prime",
							"bd in the repository's other git worktrees",
						} {
							if !strings.Contains(opt.When, want) {
								t.Errorf("embedded option %q when is missing %q: %q", opt.ID, want, opt.When)
							}
						}
						for _, want := range []string{
							"if it reports instead that the database already exists, a bd process created a new one after the move",
							"afterwards bd where in each of the repository's other git worktrees must report this .beads",
							"move that worktree's .beads/embeddeddolt aside",
						} {
							if !strings.Contains(opt.Risk, want) {
								t.Errorf("embedded option %q risk is missing %q: %q", opt.ID, want, opt.Risk)
							}
						}
						if strings.Contains(opt.When, "configured database path") {
							t.Errorf("embedded option %q when still points at a configurable database path: %q", opt.ID, opt.When)
						}
						continue
					}
					if strings.Contains(opt.Risk, "embeddeddolt") || strings.Contains(opt.Risk, "bd where") {
						t.Errorf("server option %q risk carries the embedded move-window checks: %q", opt.ID, opt.Risk)
					}
					if !slices.Equal(opt.Commands, []string{"bd bootstrap"}) {
						t.Errorf("server option %q commands = %q, want only bd bootstrap behind its prerequisites", opt.ID, opt.Commands)
					}
					for _, want := range []string{"host", "stopped", "data_dir", "restarted", "downtime"} {
						if !strings.Contains(opt.When, want) {
							t.Errorf("server option %q when is missing prerequisite %q: %q", opt.ID, want, opt.When)
						}
					}
					if check, move := strings.Index(opt.When, "bd config get sync.remote"), strings.Index(opt.When, "data_dir"); check < 0 || check > move {
						t.Errorf("server option %q when does not check sync.remote before the data_dir move: %q", opt.ID, opt.When)
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
		// Each marker alone is enough: the Dolt store sets ServerMode, the
		// shared gate sets Shared, and the uow provider sets Proxied, and no
		// caller is required to set the others.
		{"shared alone", func(e *RemoteMigrateGateError) { e.Shared = true }},
		{"proxied alone", func(e *RemoteMigrateGateError) { e.Proxied = true }},
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
				for _, banned := range []string{".beads/embeddeddolt", "embeddeddolt", "mv -f", "mkdir -p"} {
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
					t.Fatalf("bd bootstrap must follow the data_dir move:\n%s", msg)
				}
				assertAdoptRemoteChecks(t, msg, idx, idx+strings.Index(msg[idx:], "        bd bootstrap\n"))
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
				for _, want := range []string{"never replaces an existing database", "moved aside first", "remote_migrate_gate.options", "operator", "do NOT auto-run",
					// #6687 review: the remote check before the move and the
					// plan check after it, as in the options, with the form
					// bootstrap prints a git-forge remote in.
					"before the move bd config get sync.remote must print the remote to adopt", "only a bootstrap plan that clones from it",
					"git+https:// or git+ssh:// form, without credentials"} {
					if !strings.Contains(d, want) {
						t.Errorf("directive is missing %q: %s", want, d)
					}
				}
				for _, banned := range []string{"until an automatic fast-forward lands", "re-clones and replaces", "bd migrate --force"} {
					if strings.Contains(d, banned) {
						t.Errorf("directive still says %q: %s", banned, d)
					}
				}
				if tc.name == "adopt-ff" {
					// Only the Dolt-history fast-forward is loss-free; the
					// re-clone an operator-directed adopt runs is not.
					for _, want := range []string{"no local commit would be lost", "did not run on this open", "clone-local data such as wisps"} {
						if !strings.Contains(d, want) {
							t.Errorf("adopt-ff directive is missing %q: %s", want, d)
						}
					}
					if strings.Contains(d, "adopting is loss-free") {
						t.Errorf("adopt-ff directive still says adopting by re-clone is loss-free: %s", d)
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
