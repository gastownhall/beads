package dolt

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestApplyConfigDefaultsAppliesDialTimeoutLadder pins ga-g8lsb4: the
// fail-fast dial budget is a knob on the same ladder as the pool deadlines,
// applied at the constructor seam every store open shares.
func TestApplyConfigDefaultsAppliesDialTimeoutLadder(t *testing.T) {
	t.Run("env var sets the dial budget", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DIAL_TIMEOUT", "5s")
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir()}
		applyConfigDefaults(cfg)
		if cfg.DialTimeout != 5*time.Second || dialTimeoutFor(cfg) != 5*time.Second {
			t.Fatalf("DialTimeout = %v (for=%v), want 5s from BEADS_DOLT_DIAL_TIMEOUT", cfg.DialTimeout, dialTimeoutFor(cfg))
		}
	})
	t.Run("caller-set budget wins over the env var", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DIAL_TIMEOUT", "5s")
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir(), DialTimeout: 250 * time.Millisecond}
		applyConfigDefaults(cfg)
		if cfg.DialTimeout != 250*time.Millisecond {
			t.Fatalf("DialTimeout = %v, want the caller's 250ms", cfg.DialTimeout)
		}
	})
	t.Run("unset knob leaves the 500ms loopback default", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DIAL_TIMEOUT", "")
		config.ResetForTesting()
		t.Cleanup(config.ResetForTesting)
		cfg := &Config{ServerMode: true, Database: "ladder", Path: t.TempDir()}
		applyConfigDefaults(cfg)
		if cfg.DialTimeout != 0 || dialTimeoutFor(cfg) != defaultDialTimeout {
			t.Fatalf("DialTimeout = %v (for=%v), want 0 resolving to %v", cfg.DialTimeout, dialTimeoutFor(cfg), defaultDialTimeout)
		}
	})
}

// TestDialServerFailFastRetriesOnlyATimeout pins the retry rule: a refused
// dial fails once, immediately, and the error carries the budget and the
// measured time; a dial that runs out its budget is tried a second time
// before it is reported.
func TestDialServerFailFastRetriesOnlyATimeout(t *testing.T) {
	t.Run("refused dial is not retried", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close() // the port is now closed: an immediate refusal
		_, err = dialServerFailFast("tcp", addr, 2*time.Second)
		if err == nil {
			t.Fatal("dial of a closed port succeeded")
		}
		if isDialTimeout(err) || !strings.Contains(err.Error(), "1 attempt(s)") || !strings.Contains(err.Error(), "dial budget 2s") {
			t.Fatalf("err = %q, want a single-attempt refusal carrying the budget", err)
		}
	})
	t.Run("timed-out dial is retried once", func(t *testing.T) {
		// A listener whose backlog is never drained accepts SYNs only until
		// the kernel backlog fills; a non-routable address is the portable
		// way to force the timeout, and if this host answers it immediately
		// (a sandbox with no route) the case is skipped, not faked.
		started := time.Now()
		_, err := dialServerFailFast("tcp", "10.255.255.1:3307", 150*time.Millisecond)
		if err == nil {
			t.Skip("10.255.255.1:3307 answered; cannot force a dial timeout here")
		}
		if !isDialTimeout(err) {
			t.Skipf("dial failed immediately (%v); cannot force a dial timeout here", err)
		}
		if !strings.Contains(err.Error(), "2 attempt(s)") {
			t.Fatalf("err = %q, want the second attempt recorded", err)
		}
		if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
			t.Fatalf("elapsed %v, want at least two 150ms budgets", elapsed)
		}
	})
}
