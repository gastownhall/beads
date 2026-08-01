package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func TestDoltStopHelpStatesForceExecutableSafety(t *testing.T) {
	normalizedHelp := strings.Join(strings.Fields(strings.ToLower(doltStopCmd.Long)), " ")
	for _, want := range []string{
		"force still refuses to signal",
		"executable identity cannot be matched to bd or dolt",
	} {
		if !strings.Contains(normalizedHelp, want) {
			t.Errorf("dolt stop long help missing %q:\n%s", want, doltStopCmd.Long)
		}
	}
	flag := doltStopCmd.Flags().Lookup("force")
	if flag == nil || !strings.Contains(flag.Usage, "bd/dolt executable match") {
		t.Errorf("dolt stop --force usage does not state executable match: %+v", flag)
	}
}

func TestNewForcedDoltStopResultReportsCompletedActions(t *testing.T) {
	shutdownErr := errors.New("verified shutdown refused")
	result := newForcedDoltStopResult(shutdownErr, proxy.ForceStopReport{
		RecordPath:      "/tmp/proxy.pid",
		PID:             42,
		Executable:      "bd",
		RecordFound:     true,
		LockWasHeld:     true,
		SignalSent:      true,
		QuarantinedPath: "/tmp/proxy.pid.stale-1",
	}, nil)

	if !result.Stopped || !result.Force || !result.ForcedRecovery {
		t.Fatalf("stop state = %+v, want completed forced recovery", result)
	}
	if result.Verified == nil || *result.Verified {
		t.Fatalf("verified = %v, want explicit false", result.Verified)
	}
	if result.ExecutableVerified == nil || !*result.ExecutableVerified {
		t.Fatalf("executable_verified = %v, want explicit true", result.ExecutableVerified)
	}
	if !result.SignalSent || result.ProcessLeftAlone {
		t.Fatalf("process action = %+v, want signaled and not left alone", result)
	}
	if result.RecordLeftAlone || result.QuarantinedPath == "" {
		t.Fatalf("record action = %+v, want quarantined and not left alone", result)
	}
}

func TestNewForcedDoltStopResultReportsRefusalWithoutClaimingActions(t *testing.T) {
	shutdownErr := errors.New("verified shutdown refused")
	forceErr := errors.New("executable basename is \"sleep\", want bd or dolt")
	result := newForcedDoltStopResult(shutdownErr, proxy.ForceStopReport{
		RecordPath:  "/tmp/proxy.pid",
		PID:         43,
		Executable:  "sleep",
		RecordFound: true,
	}, forceErr)

	if result.Stopped || result.Error != forceErr.Error() {
		t.Fatalf("stop state = %+v, want refused with error", result)
	}
	if result.ExecutableVerified == nil || *result.ExecutableVerified {
		t.Fatalf("executable_verified = %v, want explicit false", result.ExecutableVerified)
	}
	if result.SignalSent || result.ProcessWasGone || !result.ProcessLeftAlone {
		t.Fatalf("process action = %+v, want left alone", result)
	}
	if !result.RecordLeftAlone || result.QuarantinedPath != "" {
		t.Fatalf("record action = %+v, want unchanged", result)
	}
}
