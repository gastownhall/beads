package dolt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestApplyConfigDefaultsAppliesPoolTimeoutLadder pins the pool-deadline knobs
// at the seam every DoltStore open shares. applyResolvedConfig applied the
// BEADS_DOLT_POOL_READ_TIMEOUT / dolt.pool-read-timeout ladder (#5089), but
// only callers of NewFromConfig* pass through it: the CLI's own store open
// hand-builds its Config and goes straight to New →
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

	// The config.yaml rung, on the constructor path. The subtests above only
	// reach the env rung, so poolTimeoutFromConfig's dir fallback -- the rung a
	// library consumer that never called config.Initialize depends on -- was
	// pinned only for applyResolvedConfig (TestApplyResolvedConfig in
	// open_test.go). This mirrors that subtest through applyConfigDefaults,
	// which reads the directory off cfg.BeadsDir rather than a parameter.
	t.Run("config.yaml populates the pool deadlines on the constructor path", func(t *testing.T) {
		// Premise, as in the base subtest: with a populated global viper the
		// dir-fallback rung under test is not the one answering and this would
		// pass vacuously.
		config.ResetForTesting()
		t.Cleanup(config.ResetForTesting)
		if config.GetString("dolt.pool-read-timeout") != "" || config.GetString("dolt.pool-write-timeout") != "" {
			t.Fatal("global viper unexpectedly configured; this subtest's premise is broken")
		}
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		beadsDir := t.TempDir()
		body := "dolt:\n  pool-read-timeout: 120s\n  pool-write-timeout: 45\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("writing config.yaml: %v", err)
		}
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir(), BeadsDir: beadsDir}

		applyConfigDefaults(cfg)

		if cfg.PoolReadTimeout != 120*time.Second {
			t.Fatalf("PoolReadTimeout = %v, want 120s from config.yaml", cfg.PoolReadTimeout)
		}
		if cfg.PoolWriteTimeout != 45*time.Second {
			t.Fatalf("PoolWriteTimeout = %v, want 45s from config.yaml (bare number = seconds)", cfg.PoolWriteTimeout)
		}
		if dsn := buildServerDSN(cfg, cfg.Database); !strings.Contains(dsn, "readTimeout=2m0s") {
			t.Fatalf("buildServerDSN did not carry the config.yaml deadline: %s", dsn)
		}
	})
}
