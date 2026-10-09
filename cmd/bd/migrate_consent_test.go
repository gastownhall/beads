package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// renderMigrateConsentJSON routes a wrapped consent refusal through
// renderTypedOpenError in --json mode, the way a refused store open reaches
// it, and returns the decoded payload and the raw text.
func renderMigrateConsentJSON(t *testing.T, refusal *schema.MigrateConsentError) (map[string]interface{}, string) {
	t.Helper()
	pinJSONOutput(t, true)
	t.Setenv("BD_JSON_ENVELOPE", "")

	var handled bool
	out := captureNoticeStderr(t, func() {
		handled = renderTypedOpenError(fmt.Errorf("failed to open database: %w", refusal))
	})
	if !handled {
		t.Fatal("renderTypedOpenError did not handle a wrapped MigrateConsentError")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, out)
	}
	return parsed, out
}

// migrateConsentOptions returns migrate_consent.options keyed by id.
func migrateConsentOptions(t *testing.T, parsed map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	obj, ok := parsed["migrate_consent"].(map[string]interface{})
	if !ok {
		t.Fatalf("migrate_consent key missing or wrong type: %T", parsed["migrate_consent"])
	}
	rawOpts, ok := obj["options"].([]interface{})
	if !ok {
		t.Fatalf("migrate_consent.options missing or wrong type: %T", obj["options"])
	}
	byID := map[string]map[string]interface{}{}
	for _, raw := range rawOpts {
		o, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("option has wrong type: %T", raw)
		}
		id, _ := o["id"].(string)
		byID[id] = o
	}
	return byID
}

func optionCommands(t *testing.T, o map[string]interface{}) []string {
	t.Helper()
	raw, ok := o["commands"].([]interface{})
	if !ok {
		t.Fatalf("option %v: commands missing or not an array: %T", o["id"], o["commands"])
	}
	commands := make([]string, 0, len(raw))
	for _, c := range raw {
		s, _ := c.(string)
		commands = append(commands, s)
	}
	return commands
}

// TestHandleMigrateConsentJSON_Shape pins the agent-facing refusal to the
// remote-migrate gate's convention (#4259). Migrating is one-way, so the
// payload must not hand an agent a runnable migration as "the fix": the error
// names the refusal, the hint is the non-runnable directive, and the migrate
// command appears exactly once, inside the operator-gated migrate option. The
// consent environment variable must not appear at all.
func TestHandleMigrateConsentJSON_Shape(t *testing.T) {
	orig := globalFlag
	globalFlag = false
	t.Cleanup(func() { globalFlag = orig })

	refusal := &schema.MigrateConsentError{CurrentVersion: 65, LatestVersion: 69, Pending: 4}
	parsed, raw := renderMigrateConsentJSON(t, refusal)

	if got := parsed["error"]; got != refusal.Refusal() {
		t.Errorf("error = %v, want the remedy-free refusal %q", got, refusal.Refusal())
	}
	if got := parsed["hint"]; got != refusal.AgentDirective() {
		t.Errorf("hint = %v, want the directive %q", got, refusal.AgentDirective())
	}
	for _, field := range []string{"error", "hint"} {
		s, _ := parsed[field].(string)
		if strings.Contains(s, schema.SharedConsentCommand) {
			t.Errorf("%s must not carry the runnable migration %q: %q", field, schema.SharedConsentCommand, s)
		}
	}
	if strings.Contains(raw, "BD_ALLOW") {
		t.Errorf("the payload must not name a consent environment variable:\n%s", raw)
	}
	if n := strings.Count(raw, schema.SharedConsentCommand); n != 1 {
		t.Errorf("%q appears %d times, want exactly once (inside options[migrate]):\n%s",
			schema.SharedConsentCommand, n, raw)
	}

	obj := parsed["migrate_consent"].(map[string]interface{})
	for key, want := range map[string]float64{"current_version": 65, "required_version": 69, "pending": 4} {
		if got, ok := obj[key].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v", key, obj[key], want)
		}
	}
	if got := obj["severity"]; got != "blocking" {
		t.Errorf("severity = %v, want \"blocking\"", got)
	}
	if got := obj["human_decision_required"]; got != true {
		t.Errorf("human_decision_required = %v, want true", got)
	}

	opts := migrateConsentOptions(t, parsed)
	if len(opts) != 2 || opts["migrate"] == nil || opts["keep"] == nil {
		t.Fatalf("options = %v, want exactly migrate and keep", opts)
	}
	if got := optionCommands(t, opts["migrate"]); len(got) != 1 || got[0] != schema.SharedConsentCommand {
		t.Errorf("options[migrate].commands = %v, want [%q]", got, schema.SharedConsentCommand)
	}
	if got := optionCommands(t, opts["keep"]); len(got) != 0 {
		t.Errorf("options[keep].commands = %v, want none", got)
	}
	if risk, _ := opts["migrate"]["risk"].(string); !strings.Contains(risk, "one-way") {
		t.Errorf("options[migrate].risk must name the one-way risk: %q", risk)
	}
	for id, o := range opts {
		if when, _ := o["when"].(string); when == "" {
			t.Errorf("options[%s].when is empty; every option is gated on its precondition", id)
		}
	}
}

// TestHandleMigrateConsentJSON_GlobalRetarget pins the --global retarget: the
// refused open aimed at beads_global, so the project-scoped verb would migrate
// the wrong database and leave the refusal in place. The command lives only
// inside the option, so that is where the retarget has to land.
func TestHandleMigrateConsentJSON_GlobalRetarget(t *testing.T) {
	orig := globalFlag
	globalFlag = true
	t.Cleanup(func() { globalFlag = orig })

	parsed, raw := renderMigrateConsentJSON(t, &schema.MigrateConsentError{CurrentVersion: 65, LatestVersion: 69, Pending: 4})

	opts := migrateConsentOptions(t, parsed)
	if got := optionCommands(t, opts["migrate"]); len(got) != 1 || got[0] != schema.SharedConsentCommandGlobal {
		t.Errorf("options[migrate].commands = %v, want [%q]", got, schema.SharedConsentCommandGlobal)
	}
	if strings.Contains(raw, `"`+schema.SharedConsentCommand+`"`) {
		t.Errorf("--global payload still names the project-scoped %q:\n%s", schema.SharedConsentCommand, raw)
	}
}

// TestRenderTypedOpenErrorMigrateConsentText covers the human path: the full
// UserMessage, plus the --global verb when the invocation targeted the global
// database, since the message itself cannot know which database was refused.
func TestRenderTypedOpenErrorMigrateConsentText(t *testing.T) {
	pinJSONOutput(t, false)
	orig := globalFlag
	t.Cleanup(func() { globalFlag = orig })

	refusal := &schema.MigrateConsentError{CurrentVersion: 65, LatestVersion: 69, Pending: 4}
	wrapped := fmt.Errorf("failed to open database: %w", refusal)

	render := func() string {
		t.Helper()
		var handled bool
		out := captureNoticeStderr(t, func() { handled = renderTypedOpenError(wrapped) })
		if !handled {
			t.Fatal("renderTypedOpenError did not handle a wrapped MigrateConsentError")
		}
		return out
	}

	globalFlag = false
	if out := render(); out != refusal.UserMessage() {
		t.Errorf("without --global the refusal is the UserMessage alone, got:\n%s", out)
	}

	globalFlag = true
	out := render()
	if !strings.HasPrefix(out, refusal.UserMessage()) {
		t.Errorf("under --global the UserMessage must still lead, got:\n%s", out)
	}
	if !strings.Contains(out, schema.SharedConsentCommandGlobal) {
		t.Errorf("under --global the refusal must name %q, got:\n%s", schema.SharedConsentCommandGlobal, out)
	}
}

// TestLocalMigrateConsentScope pins which command lines consent to migrating
// an existing database. Only the verb that names the migration does, plus
// --force on either migrate command. Bare `bd migrate` reconciles metadata and
// never needed the gate open, so letting it consent made
// `bd migrate --update-repo-id` (repo-fingerprint surgery) a one-way schema
// migration as a side effect.
//
// Each line is resolved through the same cobra lookup and fed to the same
// predicates as the root pre-run (isForcedMigrate, isMigrateConsentCommand),
// then the real gate runs against an existing database one migration behind.
func TestLocalMigrateConsentScope(t *testing.T) {
	for _, tt := range []struct {
		cmdline  string
		consents bool
	}{
		{cmdline: "bd migrate schema", consents: true},
		{cmdline: "bd migrate schema --force", consents: true},
		{cmdline: "bd migrate --force", consents: true},
		{cmdline: "bd migrate", consents: false},
		{cmdline: "bd migrate --update-repo-id", consents: false},
		{cmdline: "bd migrate --inspect", consents: false},
		{cmdline: "bd migrate --dry-run", consents: false},
		{cmdline: "bd list", consents: false},
	} {
		t.Run(tt.cmdline, func(t *testing.T) {
			err := runLocalConsentGate(t, tt.cmdline, tt.consents)
			if tt.consents {
				// Programmatic consent short-circuits the gate before its version
				// reads, and those reads would fail here with "migrate consent:"
				// (the mock expects nothing). Whatever MigrateUp hit past the gate
				// is not this test's concern.
				if schema.IsMigrateConsentError(err) || (err != nil && strings.Contains(err.Error(), "migrate consent:")) {
					t.Fatalf("%q must consent, but the gate ran: %v", tt.cmdline, err)
				}
				return
			}
			if !schema.IsMigrateConsentError(err) {
				t.Fatalf("%q must not consent; want the consent refusal, got %T: %v", tt.cmdline, err, err)
			}
		})
	}
}

// runLocalConsentGate replays the root pre-run's two consent lines for
// cmdline and runs MigrateUp. A consenting line must not reach the gate's
// reads, so its mock expects nothing; a refusing one gets the gate's reads
// for an existing database one migration behind.
func runLocalConsentGate(t *testing.T, cmdline string, consents bool) error {
	t.Helper()
	t.Setenv(schema.AllowMigrateEnv, "")
	t.Setenv(schema.AllowRemoteMigrateEnv, "")

	args := strings.Fields(strings.TrimPrefix(cmdline, "bd "))
	target, rest, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("%q does not resolve to a command: %v", cmdline, err)
	}
	if err := target.ParseFlags(rest); err != nil {
		t.Fatalf("%q: flags do not parse: %v", cmdline, err)
	}
	t.Cleanup(func() {
		// Package-level cobra singletons: a parsed flag would outlive this
		// test and silently consent for the next one.
		target.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		schema.SetForceAllowRemoteMigrate(false)
		schema.SetLocalMigrateConsent(false)
	})

	// The two lines the root pre-run runs, verbatim in effect.
	schema.SetForceAllowRemoteMigrate(isForcedMigrate(target))
	schema.SetLocalMigrateConsent(isMigrateConsentCommand(target))

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !consents {
		for range 2 {
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.tables`).
				WithArgs("schema_migrations").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
				WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(schema.LatestVersion() - 1))
		}
	}

	_, gateErr := schema.MigrateUp(context.Background(), db)
	if !consents {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations for %q: %v", cmdline, err)
		}
	}
	return gateErr
}
