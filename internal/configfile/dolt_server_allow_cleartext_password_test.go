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

// TestGetDoltServerAllowCleartextPasswordChecked exercises the paired
// accessor that every server-DSN builder is supposed to call instead of the
// raw GetDoltServerAllowCleartextPassword, covering both metadata.json/env
// combinations and the env override on each of the two settings it pairs.
func TestGetDoltServerAllowCleartextPasswordChecked(t *testing.T) {
	t.Run("unset returns false, no error", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", "")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "")
		c := &Config{}
		got, err := c.GetDoltServerAllowCleartextPasswordChecked()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("got true, want false")
		}
	})

	t.Run("allow without tls is refused", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", "")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "")
		c := &Config{DoltServerAllowCleartextPassword: true}
		got, err := c.GetDoltServerAllowCleartextPasswordChecked()
		if err == nil {
			t.Fatal("expected error for cleartext without TLS")
		}
		if got {
			t.Error("got true on the refused path, want false")
		}
	})

	t.Run("allow with tls is accepted", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", "")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "")
		c := &Config{DoltServerAllowCleartextPassword: true, DoltServerTLS: true}
		got, err := c.GetDoltServerAllowCleartextPasswordChecked()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Error("got false, want true")
		}
	})

	t.Run("env override combinations", func(t *testing.T) {
		tests := []struct {
			name         string
			cleartextEnv string
			tlsEnv       string
			metaCT       bool
			metaTLS      bool
			wantAllow    bool
			wantErr      bool
		}{
			{"env cleartext=1 with env tls=1", "1", "1", false, false, true, false},
			{"env cleartext=true with env tls=true", "true", "true", false, false, true, false},
			{"env cleartext=1 without tls anywhere is refused", "1", "", false, false, false, true},
			{"env cleartext=0 overrides metadata true", "0", "", true, true, false, false},
			{"env tls=1 pairs with metadata cleartext true", "", "1", true, false, true, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Setenv("BEADS_DOLT_SERVER_ALLOW_CLEARTEXT_PASSWORD", tt.cleartextEnv)
				t.Setenv("BEADS_DOLT_SERVER_TLS", tt.tlsEnv)
				c := &Config{DoltServerAllowCleartextPassword: tt.metaCT, DoltServerTLS: tt.metaTLS}
				got, err := c.GetDoltServerAllowCleartextPasswordChecked()
				if tt.wantErr && err == nil {
					t.Fatal("expected error, got nil")
				}
				if !tt.wantErr && err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tt.wantAllow {
					t.Errorf("got %v, want %v", got, tt.wantAllow)
				}
			})
		}
	})
}
