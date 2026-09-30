package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// captureNoticeStderr runs fn with os.Stderr redirected and returns what it
// wrote.
func captureNoticeStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

// TestIsSchemaMigrateVerbScope pins which invocations count as consent to
// migrate a shared database (gastownhall/beads#5920). Only `bd migrate schema`
// does. Bare `bd migrate` reconciles version/repo-id metadata and never
// applies a schema migration itself, and its flag modes are further still from
// schema work — `bd migrate --update-repo-id` is repo-fingerprint surgery, and
// treating it as consent would let a repo-ID update promote the schema for
// every co-resident client as a side effect.
func TestIsSchemaMigrateVerbScope(t *testing.T) {
	for _, tt := range []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{name: "migrate schema consents", cmd: migrateSchemaCmd, want: true},
		{name: "bare migrate does not", cmd: migrateCmd, want: false},
		{name: "migrate sync does not", cmd: migrateSyncCmd, want: false},
		{name: "an unrelated command does not", cmd: rootCmd, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSchemaMigrateVerb(tt.cmd); got != tt.want {
				t.Fatalf("isSchemaMigrateVerb(%q) = %t, want %t", tt.cmd.Name(), got, tt.want)
			}
		})
	}
}

// TestNoticeSharedMigrateRefusalPerDecision covers the one-shot version-bump
// notice. It fires once per upgrade (.local_version is consumed in the same
// pre-run), so the remedy it names has to be right for THIS refusal — the gate
// returns one error type for several decisions whose correct actions differ,
// and the migrate verb does not unlock the remote-backed ones at all.
func TestNoticeSharedMigrateRefusalPerDecision(t *testing.T) {
	// The notice self-suppresses under --json, so if jsonOutput arrives here
	// already true, every case below reads as "the notice printed nothing" —
	// which is exactly how a leak from an earlier test in the package
	// presents, and is how this test failed in full-package runs while passing
	// under a narrow -run. Pin it, so a failure here can only ever be about
	// this code.
	pinJSONOutput(t, false)

	for _, tt := range []struct {
		decision string
		want     string
		absent   string
	}{
		{decision: "shared-no-remote", want: "bd migrate schema"},
		{decision: "adopt", want: "bd bootstrap", absent: "bd migrate schema"},
		{decision: "adopt-ff", want: "bd bootstrap", absent: "bd migrate schema"},
		{decision: "fork-skew", want: "bd doctor", absent: "bd migrate schema"},
		{decision: "", want: "bd migrate", absent: "bd migrate schema"},
	} {
		name := tt.decision
		if name == "" {
			name = "blunt stop"
		}
		t.Run(name, func(t *testing.T) {
			out := captureNoticeStderr(t, func() {
				noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
					CurrentVersion: 65, LatestVersion: 66, Pending: 1,
					Decision: tt.decision,
				})
			})
			if !strings.Contains(out, tt.want) {
				t.Errorf("notice for %q missing %q:\n%s", tt.decision, tt.want, out)
			}
			if tt.absent != "" && strings.Contains(out, tt.absent) {
				t.Errorf("notice for %q must not prescribe %q (the verb does not unlock this arm):\n%s",
					tt.decision, tt.absent, out)
			}
		})
	}

	// --json puts a machine-readable gate block on this same stream a moment
	// later; prose prepended to it makes the documented contract unparseable.
	t.Run("suppressed in json mode", func(t *testing.T) {
		pinJSONOutput(t, true)
		out := captureNoticeStderr(t, func() {
			noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			})
		})
		if out != "" {
			t.Errorf("notice must be silent in --json mode, got:\n%s", out)
		}
	})

	t.Run("global flag names the global remedy", func(t *testing.T) {
		orig := globalFlag
		globalFlag = true
		defer func() { globalFlag = orig }()
		out := captureNoticeStderr(t, func() {
			noticeSharedMigrateRefusal(&schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			})
		})
		if !strings.Contains(out, schema.SharedConsentCommandGlobal) {
			t.Errorf("notice under --global must name %q:\n%s", schema.SharedConsentCommandGlobal, out)
		}
	})

	t.Run("an untyped error prints nothing", func(t *testing.T) {
		out := captureNoticeStderr(t, func() { noticeSharedMigrateRefusal(io.EOF) })
		if out != "" {
			t.Errorf("only a gate refusal should produce a notice, got:\n%s", out)
		}
	})
}

// TestSharedRefusalPrescribesAWorkingVerb closes the loop the review flagged:
// the shared-store refusal names a command, and that command must actually run
// wherever the refusal can appear. In proxied-server mode `bd migrate schema`
// used to hard-refuse, so the only remedy the gate offered there was a dead
// end — worse, the root pre-run would have consumed the consent on the open
// before RunE ever printed "not supported".
//
// The pairing is checked structurally rather than by driving a proxied
// workspace: the command the gate prescribes IS SharedConsentCommand, and the
// verb that grants consent for it is migrateSchemaCmd.
func TestSharedRefusalPrescribesAWorkingVerb(t *testing.T) {
	refusal := &schema.RemoteMigrateGateError{
		CurrentVersion: 65, LatestVersion: 66, Pending: 1,
		Decision: "shared-no-remote", Shared: true,
	}

	if got := refusal.EscapeHint(); got != schema.SharedConsentCommand {
		t.Fatalf("EscapeHint = %q, want %q", got, schema.SharedConsentCommand)
	}
	opts := refusal.Options()
	if len(opts) != 1 || len(opts[0].Commands) != 1 || opts[0].Commands[0] != schema.SharedConsentCommand {
		t.Fatalf("Options = %+v, want the single %q remedy", opts, schema.SharedConsentCommand)
	}
	if !strings.Contains(refusal.UserMessage(), schema.SharedConsentCommand) {
		t.Errorf("UserMessage must name the remedy:\n%s", refusal.UserMessage())
	}

	// The prescribed command is `bd <path of migrateSchemaCmd>`, and that is
	// exactly the command isSchemaMigrateVerb grants consent for.
	wantPath := "bd " + strings.TrimPrefix(migrateSchemaCmd.CommandPath(), rootCmd.Name()+" ")
	if wantPath != schema.SharedConsentCommand {
		t.Fatalf("the gate prescribes %q but the consenting verb is %q", schema.SharedConsentCommand, wantPath)
	}
	if !isSchemaMigrateVerb(migrateSchemaCmd) {
		t.Fatal("the command the gate prescribes must be the one that grants consent")
	}
}

// TestPrintGlobalDatabaseConsentHint pins the one fact the gate's own block
// cannot know: which database the invocation targeted. Under --global the open
// hits `beads_global`, so the block's unflagged `bd migrate schema` would
// migrate the project database and leave the refusal in place.
func TestPrintGlobalDatabaseConsentHint(t *testing.T) {
	orig := globalFlag
	defer func() { globalFlag = orig }()

	sharedNoRemote := &schema.RemoteMigrateGateError{
		CurrentVersion: 65, LatestVersion: 66, Pending: 1,
		Decision: "shared-no-remote", Shared: true,
	}

	globalFlag = false
	var off bytes.Buffer
	printGlobalDatabaseConsentHint(&off, sharedNoRemote)
	if off.Len() != 0 {
		t.Errorf("no hint without --global, got:\n%s", off.String())
	}

	globalFlag = true
	var on bytes.Buffer
	printGlobalDatabaseConsentHint(&on, sharedNoRemote)
	if !strings.Contains(on.String(), schema.SharedConsentCommandGlobal) {
		t.Errorf("hint must name %q:\n%s", schema.SharedConsentCommandGlobal, on.String())
	}

	// The data-behind stop on a shared store is the arm where "the same verb
	// with the same flag" is not enough: it is remote-backed by construction,
	// so the bare verb's consent is never read and a --global rewrite of it is
	// still a command that cannot succeed. Mirrors the JSON retarget.
	var behind bytes.Buffer
	printGlobalDatabaseConsentHint(&behind, &schema.RemoteMigrateGateError{
		CurrentVersion: 66, LatestVersion: 67, Pending: 1,
		FallbackReason: "data-behind", Shared: true,
	})
	if !strings.Contains(behind.String(), schema.SharedConsentCommandForcedGlobal) {
		t.Errorf("the shared data-behind hint must name %q:\n%s",
			schema.SharedConsentCommandForcedGlobal, behind.String())
	}
}

// expectRemoteBackedSharedProbe mocks the probe sequence a shared,
// remote-backed store makes before the gate decides: CurrentVersion, then
// PendingVersions (both a cursor probe plus a max-version read), then the
// remote count. With the smart router pinned off, no further query follows
// either outcome, so an unmet expectation means the gate took a path this test
// does not describe.
func expectRemoteBackedSharedProbe(mock sqlmock.Sqlmock) {
	for i := 0; i < 2; i++ {
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.tables`).
			WithArgs("schema_migrations").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
			WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
}

// runPrescribedConsent replays the root PersistentPreRunE for one command line
// and reports what the real shared gate does afterwards on a remote-backed
// store. It resolves the command through the same cobra lookup the pre-run
// uses and feeds it to the same two predicates, so a command string that is
// not actually the consenting verb cannot pass by looking like one.
func runPrescribedConsent(t *testing.T, cmdline string) error {
	t.Helper()
	t.Setenv(schema.SmartGateEnv, "0")
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	args := strings.Fields(strings.TrimPrefix(cmdline, "bd "))
	target, rest, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("the gate prescribes %q, which does not resolve to a command: %v", cmdline, err)
	}
	if target == rootCmd {
		t.Fatalf("the gate prescribes %q, which resolves to bare bd", cmdline)
	}
	if err := target.ParseFlags(rest); err != nil {
		t.Fatalf("the gate prescribes %q, whose flags do not parse: %v", cmdline, err)
	}
	t.Cleanup(func() {
		// These cobra commands are package-level singletons, so a parsed flag
		// outlives the test that parsed it and would silently consent for the
		// next one.
		target.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		schema.SetForceAllowRemoteMigrate(false)
		schema.SetSharedMigrateConsent(false)
	})

	// The two lines the root pre-run runs, verbatim in effect.
	schema.SetForceAllowRemoteMigrate(isForcedMigrate(target))
	schema.SetSharedMigrateConsent(isSchemaMigrateVerb(target) && !isPreviewCommand(target))

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteBackedSharedProbe(mock)

	var gateErr error
	_ = captureNoticeStderr(t, func() {
		gateErr = schema.CheckSharedStoreMigrateGate(context.Background(), db, "", nil, nil)
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations for %q: %v", cmdline, err)
	}
	return gateErr
}

// TestDataBehindSharedConsentCommandUnlocks is the executing twin of
// TestSharedRefusalPrescribesAWorkingVerb, for the #6575 data-behind stop on a
// shared store — the one arm where the structural pairing that test checks is
// not enough.
//
// That stop always has a remote configured (behind-ness is read from the
// remote-tracking ref), and on the remote-backed arm the bare verb's consent
// is never read: schema.sharedMigrateConsent has a single reader, inside
// sharedNoRemoteGate, which is reachable only when no remote is configured.
// So the option this refusal carries has to be checked by RUNNING its consent
// path. Asserting its wording is exactly what let a dead command ship: the
// pre-existing tests pinned the option's text and passed throughout.
//
// The command under test is taken from Options() rather than written out here,
// so this stays a test of whatever the gate actually prescribes.
func TestDataBehindSharedConsentCommandUnlocks(t *testing.T) {
	behindShared := &schema.RemoteMigrateGateError{
		CurrentVersion: 66, LatestVersion: 67, Pending: 1,
		FallbackReason: "data-behind", Shared: true,
	}

	opts := behindShared.Options()
	if len(opts) != 2 || opts[1].ID != "migrate-shared-after-pulling" || len(opts[1].Commands) != 1 {
		t.Fatalf("Options() = %+v, want pull-first then a single-command consent step", opts)
	}
	prescribed := opts[1].Commands[0]

	t.Run("the prescribed command unlocks the post-pull retry", func(t *testing.T) {
		if err := runPrescribedConsent(t, prescribed); err != nil {
			t.Fatalf("the gate prescribes %q for this state, but running its consent path still refuses: %v",
				prescribed, err)
		}
	})

	// The counterfactual, and the regression pin: the command that shipped.
	// If this ever passes, the consent read has moved and the option above can
	// go back to the bare verb.
	t.Run("the bare verb does not, which is why the option carries --force", func(t *testing.T) {
		err := runPrescribedConsent(t, schema.SharedConsentCommand)
		if err == nil {
			t.Fatal("the bare verb now unlocks a remote-backed shared store; revisit SharedConsentCommandForced")
		}
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("want the gate refusal, got %T: %v", err, err)
		}
	})

	// Every surface that names a consent command for this stop must name the
	// one just executed — the finding was four surfaces agreeing on a dead
	// command, so agreement alone is not the property worth pinning.
	t.Run("the prose surfaces name the executed command", func(t *testing.T) {
		for name, surface := range map[string]string{
			"UserMessage":    behindShared.UserMessage(),
			"AgentDirective": behindShared.AgentDirective(),
		} {
			if !strings.Contains(surface, prescribed) {
				t.Errorf("%s does not name the prescribed consent command %q:\n%s", name, prescribed, surface)
			}
		}
	})
}
