package main

import (
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// newOlderThanTestCmd registers the "older-than" flag exactly as
// reclaimCmd's init() does, so the parser under test matches production.
func newOlderThanTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "reclaim", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().Duration("older-than", 2*issueops.DefaultLeaseTTL,
		"Only reclaim leases that expired at least this long ago (grace window)")
	return cmd
}

// TestResolveReclaimOlderThan pins the decoupling fix from PR #5470 review
// point 2: --older-than's registered default is frozen at command-
// registration time from the compiled issueops.DefaultLeaseTTL constant, so
// it cannot see a deployment's "lease.ttl"/BD_LEASE_TTL override. Without
// resolveReclaimOlderThan recomputing the default at run time, a deployment
// that widened its claim TTL would silently keep a grace window sized for
// the old, narrower TTL.
func TestResolveReclaimOlderThan(t *testing.T) {
	t.Run("no override, no flag: 2x the compiled default", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		cmd := newOlderThanTestCmd()
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatal(err)
		}
		got, err := resolveReclaimOlderThan(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := 2 * issueops.DefaultLeaseTTL
		if got != want {
			t.Errorf("resolveReclaimOlderThan() = %v, want %v", got, want)
		}
	})

	t.Run("deployment override, no flag: follows 2x the effective TTL", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "4h")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		cmd := newOlderThanTestCmd()
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatal(err)
		}
		got, err := resolveReclaimOlderThan(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := 8 * time.Hour
		if got != want {
			t.Errorf("resolveReclaimOlderThan() = %v, want 2x the 4h override = %v", got, want)
		}
	})

	t.Run("explicit --older-than wins over the deployment override", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "4h")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		cmd := newOlderThanTestCmd()
		if err := cmd.ParseFlags([]string{"--older-than", "10m"}); err != nil {
			t.Fatal(err)
		}
		got, err := resolveReclaimOlderThan(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 10*time.Minute {
			t.Errorf("resolveReclaimOlderThan() = %v, want the explicit 10m", got)
		}
	})

	t.Run("explicit negative --older-than is rejected", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		cmd := newOlderThanTestCmd()
		if err := cmd.ParseFlags([]string{"--older-than", "-1s"}); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveReclaimOlderThan(cmd); err == nil {
			t.Error("resolveReclaimOlderThan() with negative --older-than = nil error, want rejection")
		}
	})
}
