package dolt

import (
	"github.com/steveyegge/beads/internal/config"
	"strings"
	"testing"
	"time"
)

// TestApplyConfigDefaultsAppliesPoolTimeoutLadder pins the pool-deadline knobs
// at the seam every DoltStore open shares. applyResolvedConfig applied the
// BEADS_DOLT_POOL_READ_TIMEOUT / dolt.pool-read-timeout ladder (#5089), but
// only callers of NewFromConfig* pass through it: the CLI's own store open and
// bd serve's provider hand-build their Config and go straight to New →
// applyConfigDefaults, so every `bd` command in server mode kept the built-in
// 10s deadline whatever the knob said (gastownhall/beads#6144). New is the one
// constructor all of them call, so the ladder has to hold there.
func TestApplyConfigDefaultsAppliesPoolTimeoutLadder(t *testing.T) {
	t.Run("env vars populate the pool deadlines on the constructor path", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "90s")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "45")
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir()}

		applyConfigDefaults(cfg)

		if cfg.PoolReadTimeout != 90*time.Second {
			t.Fatalf("PoolReadTimeout = %v, want 90s from BEADS_DOLT_POOL_READ_TIMEOUT", cfg.PoolReadTimeout)
		}
		if cfg.PoolWriteTimeout != 45*time.Second {
			t.Fatalf("PoolWriteTimeout = %v, want 45s (bare number = seconds)", cfg.PoolWriteTimeout)
		}
		if dsn := buildServerDSN(cfg, cfg.Database); !strings.Contains(dsn, "readTimeout=1m30s") {
			t.Fatalf("buildServerDSN did not carry the env deadline: %s", dsn)
		}
	})

	t.Run("caller-set pool deadlines win over env vars", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "90s")
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir(), PoolReadTimeout: 2 * time.Minute}

		applyConfigDefaults(cfg)

		if cfg.PoolReadTimeout != 2*time.Minute {
			t.Fatalf("PoolReadTimeout = %v, want the caller's 2m", cfg.PoolReadTimeout)
		}
	})

	t.Run("unset knobs leave the built-in default in place", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		config.ResetForTesting() // the config.yaml rung reads package state, not just env
		t.Cleanup(config.ResetForTesting)
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir()}

		applyConfigDefaults(cfg)

		if cfg.PoolReadTimeout != 0 || cfg.PoolWriteTimeout != 0 {
			t.Fatalf("pool deadlines = %v/%v, want 0/0 so buildServerDSN applies its default", cfg.PoolReadTimeout, cfg.PoolWriteTimeout)
		}
		if dsn := buildServerDSN(cfg, cfg.Database); !strings.Contains(dsn, "readTimeout=10s") {
			t.Fatalf("buildServerDSN default deadline missing: %s", dsn)
		}
	})
}
