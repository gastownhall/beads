package dolt

import (
	"os"
	"testing"
)

// TestResolveAutoStart verifies all conditions that govern the AutoStart decision.
// Unit tests run with BEADS_TEST_MODE=1, so each case explicitly manages that
// env var to exercise the real logic paths.
func TestResolveAutoStart(t *testing.T) {
	tests := []struct {
		name             string
		testMode         string // BEADS_TEST_MODE value ("" means unset)
		autoStartEnv     string // BEADS_DOLT_AUTO_START value ("" means unset)
		gtRoot           string // GT_ROOT value ("" means unset; used to simulate IsDaemonManaged)
		doltAutoStartCfg string // dolt.auto-start value from config.yaml
		currentValue     bool   // AutoStart value supplied by caller
		wantAutoStart    bool
	}{
		{
			name:          "defaults to true for standalone user",
			testMode:      "",
			wantAutoStart: true,
		},
		{
			name:          "disabled when BEADS_TEST_MODE=1",
			testMode:      "1",
			wantAutoStart: false,
		},
		{
			name:          "disabled when IsDaemonManaged (GT_ROOT set)",
			testMode:      "",
			gtRoot:        "/fake/gt/root",
			wantAutoStart: false,
		},
		{
			name:          "disabled when BEADS_DOLT_AUTO_START=0",
			testMode:      "",
			autoStartEnv:  "0",
			wantAutoStart: false,
		},
		{
			name:          "enabled when BEADS_DOLT_AUTO_START=1",
			testMode:      "",
			autoStartEnv:  "1",
			wantAutoStart: true,
		},
		{
			name:             "disabled when dolt.auto-start=false in config",
			testMode:         "",
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=0 in config",
			testMode:         "",
			doltAutoStartCfg: "0",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=off in config",
			testMode:         "",
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
			testMode:      "",
			currentValue:  true,
			wantAutoStart: true,
		},
		{
			name:          "test mode overrides caller true",
			testMode:      "1",
			currentValue:  true,
			wantAutoStart: false,
		},
	}

	// Persist and restore env vars that the test harness may have set.
	origTestMode := os.Getenv("BEADS_TEST_MODE")
	origAutoStart := os.Getenv("BEADS_DOLT_AUTO_START")
	origGTRoot := os.Getenv("GT_ROOT")
	t.Cleanup(func() {
		restoreEnv("BEADS_TEST_MODE", origTestMode)
		restoreEnv("BEADS_DOLT_AUTO_START", origAutoStart)
		restoreEnv("GT_ROOT", origGTRoot)
	})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setOrUnsetEnv(t, "BEADS_TEST_MODE", tc.testMode)
			setOrUnsetEnv(t, "BEADS_DOLT_AUTO_START", tc.autoStartEnv)
			setOrUnsetEnv(t, "GT_ROOT", tc.gtRoot)

			got := resolveAutoStart(tc.currentValue, tc.doltAutoStartCfg)
			if got != tc.wantAutoStart {
				t.Errorf("resolveAutoStart(current=%v, configVal=%q) = %v, want %v",
					tc.currentValue, tc.doltAutoStartCfg, got, tc.wantAutoStart)
			}
		})
	}
}

func setOrUnsetEnv(t *testing.T, key, value string) {
	t.Helper()
	if value == "" {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetenv %s: %v", key, err)
		}
	} else {
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("setenv %s: %v", key, err)
		}
	}
}

func restoreEnv(key, original string) {
	if original == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, original)
	}
}
