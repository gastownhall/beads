package config

import (
	"testing"
	"time"
)

// TestDoltRemoteTimeout pins AC-7 of GH#7989: dolt.remote-timeout bounds one
// embedded remote transfer statement. A positive duration applies, the
// environment overrides the config file, and an unset, invalid, zero or
// negative value (or a read before Initialize, as an SDK caller may do) falls
// back to the five-minute default.
func TestDoltRemoteTimeout(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	cases := []struct {
		name string
		cfg  string // "" leaves the key unset
		env  string // "" leaves BD_DOLT_REMOTE_TIMEOUT unset
		want time.Duration
	}{
		{name: "unset", want: 5 * time.Minute},
		{name: "config positive", cfg: "90s", want: 90 * time.Second},
		{name: "env positive", env: "2m", want: 2 * time.Minute},
		{name: "env overrides config", cfg: "90s", env: "45s", want: 45 * time.Second},
		{name: "config invalid", cfg: "soon", want: 5 * time.Minute},
		{name: "config zero", cfg: "0s", want: 5 * time.Minute},
		{name: "config negative", cfg: "-1m", want: 5 * time.Minute},
		{name: "env invalid", env: "forever", want: 5 * time.Minute},
		{name: "env zero", env: "0", want: 5 * time.Minute},
		{name: "env negative", env: "-30s", want: 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("BD_DOLT_REMOTE_TIMEOUT", tc.env)
			}
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if tc.cfg != "" {
				Set("dolt.remote-timeout", tc.cfg)
			}
			if got := DoltRemoteTimeout(); got != tc.want {
				t.Errorf("DoltRemoteTimeout() = %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("before Initialize", func(t *testing.T) {
		saved := v
		v = nil
		t.Cleanup(func() { v = saved })

		if got := DoltRemoteTimeout(); got != 5*time.Minute {
			t.Errorf("uninitialized DoltRemoteTimeout() = %s, want 5m0s", got)
		}
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "20s")
		if got := DoltRemoteTimeout(); got != 20*time.Second {
			t.Errorf("uninitialized DoltRemoteTimeout() with env = %s, want 20s", got)
		}
	})
}
