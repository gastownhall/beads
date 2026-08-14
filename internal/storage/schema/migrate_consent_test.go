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
