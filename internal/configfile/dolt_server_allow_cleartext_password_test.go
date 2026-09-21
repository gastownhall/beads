package configfile

import (
	"encoding/json"
	"testing"
)

// TestGetDoltServerAllowCleartextPassword_MetadataOnly: with no env var set,
// the metadata.json field decides.
func TestGetDoltServerAllowCleartextPassword_MetadataOnly(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", "")

	c := &Config{DoltServerAllowCleartextPassword: true}
	if !c.GetDoltServerAllowCleartextPassword() {
		t.Error("GetDoltServerAllowCleartextPassword() = false, want true from metadata field")
	}

	c = &Config{DoltServerAllowCleartextPassword: false}
	if c.GetDoltServerAllowCleartextPassword() {
		t.Error("GetDoltServerAllowCleartextPassword() = true, want false from metadata field")
	}
}

// TestGetDoltServerAllowCleartextPassword_EnvOverridesMetadata: env wins.
func TestGetDoltServerAllowCleartextPassword_EnvOverridesMetadata(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		metadata bool
		want     bool
	}{
		{"env 1 overrides metadata false", "1", false, true},
		{"env true overrides metadata false", "true", false, true},
		{"env TRUE is case-insensitive", "TRUE", false, true},
		{"env 0 overrides metadata true", "0", true, false},
		{"env false overrides metadata true", "false", true, false},
		{"env unrecognized value reads as false", "nonsense", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", tt.envValue)
			c := &Config{DoltServerAllowCleartextPassword: tt.metadata}
			if got := c.GetDoltServerAllowCleartextPassword(); got != tt.want {
				t.Errorf("GetDoltServerAllowCleartextPassword() = %v, want %v (env=%q, metadata=%v)",
					got, tt.want, tt.envValue, tt.metadata)
			}
		})
	}
}

// TestGetDoltServerAllowCleartextPassword_EmptyEnvFallsBackToMetadata: an
// unset env var must not mask a true metadata value.
func TestGetDoltServerAllowCleartextPassword_EmptyEnvFallsBackToMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", "")
	c := &Config{DoltServerAllowCleartextPassword: true}
	if !c.GetDoltServerAllowCleartextPassword() {
		t.Error("GetDoltServerAllowCleartextPassword() = false, want true (empty env should not override metadata)")
	}
}

// TestConfig_DoltServerAllowCleartextPassword_JSONRoundTrip verifies the
// metadata.json field name: "dolt_server_allow_cleartext_password".
func TestConfig_DoltServerAllowCleartextPassword_JSONRoundTrip(t *testing.T) {
	const j = `{"database":"beads.db","dolt_server_allow_cleartext_password":true}`
	var c Config
	if err := json.Unmarshal([]byte(j), &c); err != nil {
		t.Fatalf("parsing config JSON: %v", err)
	}
	if !c.DoltServerAllowCleartextPassword {
		t.Error("DoltServerAllowCleartextPassword = false after JSON round-trip, want true")
	}
}
