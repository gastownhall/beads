package config

import (
	"testing"
	"time"
)

// TestDoltRemoteTimeout pins the GH#7989 AC-7 configuration contract: a
// positive dolt.remote-timeout / BD_DOLT_REMOTE_TIMEOUT applies; missing,
// unparsable, zero and negative values — and an uninitialized configuration —
// fall back to the five-minute default.
func TestDoltRemoteTimeout(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if got := DoltRemoteTimeout(); got != DefaultDoltRemoteTimeout {
		t.Errorf("unset dolt.remote-timeout = %s, want the %s default", got, DefaultDoltRemoteTimeout)
	}

	t.Run("positive duration applies", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "90s")
		if got := DoltRemoteTimeout(); got != 90*time.Second {
			t.Errorf("BD_DOLT_REMOTE_TIMEOUT=90s = %s, want 90s", got)
		}
	})
	t.Run("invalid value falls back", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "banana")
		if got := DoltRemoteTimeout(); got != DefaultDoltRemoteTimeout {
			t.Errorf("BD_DOLT_REMOTE_TIMEOUT=banana = %s, want the %s fallback", got, DefaultDoltRemoteTimeout)
		}
	})
	t.Run("zero falls back", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "0s")
		if got := DoltRemoteTimeout(); got != DefaultDoltRemoteTimeout {
			t.Errorf("BD_DOLT_REMOTE_TIMEOUT=0s = %s, want the %s fallback", got, DefaultDoltRemoteTimeout)
		}
	})
	t.Run("negative falls back", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "-5m")
		if got := DoltRemoteTimeout(); got != DefaultDoltRemoteTimeout {
			t.Errorf("BD_DOLT_REMOTE_TIMEOUT=-5m = %s, want the %s fallback", got, DefaultDoltRemoteTimeout)
		}
	})
	t.Run("unset with nil viper falls back", func(t *testing.T) {
		savedV := v
		v = nil
		defer func() { v = savedV }()
		if got := DoltRemoteTimeout(); got != DefaultDoltRemoteTimeout {
			t.Errorf("DoltRemoteTimeout with nil viper = %s, want the %s default", got, DefaultDoltRemoteTimeout)
		}
	})
}