package dolt

import (
	"testing"
)

// TestResolveAutoStart verifies all conditions that govern the AutoStart decision.
//
// Each subtest uses t.Setenv for env-var isolation: t.Setenv records the
// original value (including the unset state) and restores it after the test,
// correctly handling cases where a variable was previously unset vs. set to "".
// Each subtest also calls t.Chdir(t.TempDir()) so that doltserver.IsDaemonManaged()'s
// filesystem heuristics — which inspect parent directories for Gas Town path
// segments — cannot false-trigger in environments whose real CWD looks like a
// Gas Town workspace.
func TestResolveAutoStart(t *testing.T) {
	tests := []struct {
		name             string
		testMode         string // BEADS_TEST_MODE to set; "" leaves it unset/empty
		autoStartEnv     string // BEADS_DOLT_AUTO_START to set; "" leaves it unset/empty
		gtRoot           string // GT_ROOT to set; "" leaves it unset/empty
		doltAutoStartCfg string // raw value of "dolt.auto-start" from config.yaml
		currentValue     bool   // AutoStart value supplied by caller
		wantAutoStart    bool
	}{
		{
			name:          "defaults to true for standalone user",
			wantAutoStart: true,
		},
		{
			name:          "disabled when BEADS_TEST_MODE=1",
			testMode:      "1",
			wantAutoStart: false,
		},
		{
			name:          "disabled when IsDaemonManaged (GT_ROOT set)",
			gtRoot:        "/fake/gt/root",
			wantAutoStart: false,
		},
		{
			name:          "disabled when BEADS_DOLT_AUTO_START=0",
			autoStartEnv:  "0",
			wantAutoStart: false,
		},
		{
			name:          "enabled when BEADS_DOLT_AUTO_START=1",
			autoStartEnv:  "1",
			wantAutoStart: true,
		},
		{
			name:             "disabled when dolt.auto-start=false in config",
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=0 in config",
			doltAutoStartCfg: "0",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=off in config",
			doltAutoStartCfg: "off",
			wantAutoStart:    false,
		},
		{
			name:          "test mode wins over BEADS_DOLT_AUTO_START=1",
			testMode:      "1",
			autoStartEnv:  "1",
			wantAutoStart: false,
		},
		{
			name:          "caller true preserved when no overrides",
			currentValue:  true,
			wantAutoStart: true,
		},
		{
			// Caller option wins over config.yaml per NewFromConfigWithOptions contract.
			name:             "caller true wins over config.yaml opt-out",
			currentValue:     true,
			doltAutoStartCfg: "false",
			wantAutoStart:    true,
		},
		{
			name:          "test mode overrides caller true",
			testMode:      "1",
			currentValue:  true,
			wantAutoStart: false,
		},
		{
			name:          "BEADS_DOLT_AUTO_START=0 overrides caller true",
			autoStartEnv:  "0",
			currentValue:  true,
			wantAutoStart: false,
		},
		{
			name:          "IsDaemonManaged overrides caller true",
			gtRoot:        "/fake/gt/root",
			currentValue:  true,
			wantAutoStart: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Isolate CWD so filesystem heuristics in IsDaemonManaged never
			// accidentally see Gas Town path segments in the working directory.
			t.Chdir(t.TempDir())

			// t.Setenv records and restores the original state (incl. whether
			// the var was set at all) so subtests don't leak into each other.
			// BEADS_TEST_MODE is checked for exact value "1" (to enable test mode);
			// BEADS_DOLT_AUTO_START is checked for exact value "0" (to disable
			// auto-start). Setting either to "" is effectively a no-op for both checks.
			t.Setenv("BEADS_TEST_MODE", tc.testMode)
			t.Setenv("GT_ROOT", tc.gtRoot)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStartEnv)

			got := resolveAutoStart(tc.currentValue, tc.doltAutoStartCfg, false)
			if got != tc.wantAutoStart {
				t.Errorf("resolveAutoStart(current=%v, configVal=%q) = %v, want %v",
					tc.currentValue, tc.doltAutoStartCfg, got, tc.wantAutoStart)
			}
		})
	}
}
