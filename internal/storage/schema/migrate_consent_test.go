package schema

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestMain grants migration consent for the whole package: the MigrateUp
// machinery tests exercise what runs BELOW the consent gate, exactly as
// production does once an operator has consented. The consent tests below
// clear the grant themselves via resetConsentState.
func TestMain(m *testing.M) {
	SetLocalMigrateConsent(true)
	os.Exit(m.Run())
}

// resetConsentState clears every consent source for one test case (undoing
// TestMain's package-wide grant) and restores it afterwards, so consent tests
// see a clean slate and machinery tests keep their grant.
func resetConsentState(t *testing.T) {
	t.Helper()
	SetLocalMigrateConsent(false)
	SetForceAllowRemoteMigrate(false)
	t.Cleanup(func() {
		SetLocalMigrateConsent(true)
		SetForceAllowRemoteMigrate(false)
	})
	t.Setenv(AllowMigrateEnv, "")
	t.Setenv(AllowRemoteMigrateEnv, "")
}

func TestMigrateConsentDecision_FreshDB_Allows(t *testing.T) {
	resetConsentState(t)
	if err := migrateConsentDecision(0, 12); err != nil {
		t.Fatalf("decision = %v, want nil for fresh database (current=0)", err)
	}
}

func TestMigrateConsentDecision_NothingPending_Allows(t *testing.T) {
	resetConsentState(t)
	if err := migrateConsentDecision(LatestVersion(), 0); err != nil {
		t.Fatalf("decision = %v, want nil when nothing is pending", err)
	}
}

func TestMigrateConsentDecision_PendingNoConsent_Refuses(t *testing.T) {
	resetConsentState(t)
	err := migrateConsentDecision(53, 12)
	var consentErr *MigrateConsentError
	if !errors.As(err, &consentErr) {
		t.Fatalf("decision = %v, want *MigrateConsentError", err)
	}
	if consentErr.CurrentVersion != 53 || consentErr.Pending != 12 {
		t.Fatalf("error fields = v%d/%d pending, want v53/12", consentErr.CurrentVersion, consentErr.Pending)
	}
	if !IsMigrateConsentError(err) {
		t.Fatalf("IsMigrateConsentError(err) = false, want true")
	}
	msg := consentErr.UserMessage()
	for _, want := range []string{"bd migrate schema", AllowMigrateEnv} {
		if !strings.Contains(msg, want) {
			t.Fatalf("UserMessage missing %q:\n%s", want, msg)
		}
	}
}

func TestMigrateConsentDecision_LocalConsent_Allows(t *testing.T) {
	resetConsentState(t)
	SetLocalMigrateConsent(true)
	if err := migrateConsentDecision(53, 12); err != nil {
		t.Fatalf("decision = %v, want nil with local consent set", err)
	}
}

func TestMigrateConsentDecision_ForceOverride_Allows(t *testing.T) {
	resetConsentState(t)
	SetForceAllowRemoteMigrate(true)
	if err := migrateConsentDecision(53, 12); err != nil {
		t.Fatalf("decision = %v, want nil with the migrate --force override set", err)
	}
}

func TestMigrateConsentDecision_EnvConsent_Allows(t *testing.T) {
	for _, env := range []string{AllowMigrateEnv, AllowRemoteMigrateEnv} {
		for _, v := range []string{"1", "true", "TRUE"} {
			t.Run(env+"="+v, func(t *testing.T) {
				resetConsentState(t)
				t.Setenv(env, v)
				if err := migrateConsentDecision(53, 12); err != nil {
					t.Fatalf("decision = %v, want nil with %s=%s", err, env, v)
				}
			})
		}
	}
}

func TestMigrateConsentDecision_EnvFalse_Refuses(t *testing.T) {
	resetConsentState(t)
	t.Setenv(AllowMigrateEnv, "0")
	if !IsMigrateConsentError(migrateConsentDecision(53, 12)) {
		t.Fatalf("want refusal with %s=0", AllowMigrateEnv)
	}
}

func TestMigrateConsentDecision_UnparseableEnv_RefusesWithHint(t *testing.T) {
	resetConsentState(t)
	t.Setenv(AllowMigrateEnv, "yes-please")
	err := migrateConsentDecision(53, 12)
	var consentErr *MigrateConsentError
	if !errors.As(err, &consentErr) {
		t.Fatalf("decision = %v, want *MigrateConsentError", err)
	}
	if consentErr.UnrecognizedEnv != "yes-please" {
		t.Fatalf("UnrecognizedEnv = %q, want the unparseable value surfaced", consentErr.UnrecognizedEnv)
	}
	if !strings.Contains(consentErr.UserMessage(), "yes-please") {
		t.Fatalf("UserMessage does not surface the unparseable env value:\n%s", consentErr.UserMessage())
	}
}

// TestMigrateConsentError_AgentSurfacesNameNoRemedy pins the #4259 directive
// convention cmd/bd's JSON refusal is built on: Refusal and AgentDirective are
// the top-level fields an agent reads, so neither may carry a runnable
// migration or a consent env var; the migrate command lives only in the
// operator-gated "migrate" option.
func TestMigrateConsentError_AgentSurfacesNameNoRemedy(t *testing.T) {
	e := &MigrateConsentError{CurrentVersion: 65, LatestVersion: 69, Pending: 4}
	for name, s := range map[string]string{"Refusal": e.Refusal(), "AgentDirective": e.AgentDirective()} {
		for _, unwanted := range []string{SharedConsentCommand, "BD_ALLOW"} {
			if strings.Contains(s, unwanted) {
				t.Errorf("%s carries %q:\n%s", name, unwanted, s)
			}
		}
	}
	opts := e.Options()
	if len(opts) != 2 || opts[0].ID != "migrate" || opts[1].ID != "keep" {
		t.Fatalf("Options = %+v, want exactly migrate then keep", opts)
	}
	if len(opts[0].Commands) != 1 || opts[0].Commands[0] != SharedConsentCommand {
		t.Errorf("migrate option commands = %q, want [%q]", opts[0].Commands, SharedConsentCommand)
	}
	if len(opts[1].Commands) != 0 {
		t.Errorf("keep option commands = %q, want none", opts[1].Commands)
	}
}

// TestMigrateConsentError_WorkingSetWarning pins the warning a
// working-set-reconcile open prints when it continues past the refusal: it
// states the refusal and that the commit runs on the current schema, and it
// never offers the consent env var as the way forward.
func TestMigrateConsentError_WorkingSetWarning(t *testing.T) {
	e := &MigrateConsentError{CurrentVersion: 65, LatestVersion: 69, Pending: 4}
	w := e.WorkingSetWarning()
	for _, want := range []string{e.Refusal(), "Working-set reconcile command: continuing on schema v65 without"} {
		if !strings.Contains(w, want) {
			t.Errorf("WorkingSetWarning missing %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "BD_ALLOW") {
		t.Errorf("WorkingSetWarning offers a consent env var:\n%s", w)
	}
}
