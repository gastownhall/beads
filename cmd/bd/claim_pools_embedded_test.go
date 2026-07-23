//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedClaimPools covers pool-aware claiming (bd-bguz6): issues
// pre-assigned to a pool alias listed in the claim.pools config are
// claimable by any actor through the normal --claim CAS, while issues
// assigned to a real actor (or to an unconfigured alias) keep their
// anti-steal protection. Mirrors the wyvern dispatcher pattern that
// pre-assigns review beads to "fable-crew".
func TestEmbeddedClaimPools(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tp")

	cfgCmd := exec.Command(bd, "config", "set", "claim.pools", "fable-crew, night-crew")
	cfgCmd.Dir = dir
	cfgCmd.Env = bdEnv(dir)
	if out, err := cfgCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd config set claim.pools failed: %v\n%s", err, out)
	} else if strings.Contains(string(out), "not a recognized config key") {
		// The exit code alone missed this once: claim.* must be a
		// recognized namespace so the feature's setup command doesn't
		// warn every adopter.
		t.Fatalf("bd config set claim.pools warned about an unrecognized key:\n%s", out)
	}

	t.Run("claim_from_pool_succeeds", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Pool-dispatched review", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "fable-crew")

		bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "tortoise")

		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "tortoise" {
			t.Errorf("assignee after pool claim = %q, want tortoise", got.Assignee)
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("status after pool claim = %s, want in_progress", got.Status)
		}

		// The full dispatcher flow: claim from pool, work, close — no
		// reassign-then-close two-step (the friction reported in bd-bguz6).
		bdClose(t, bd, dir, issue.ID, "--reason", "reviewed", "--actor", "tortoise")
		gotClosed := bdShow(t, bd, dir, issue.ID)
		if gotClosed.Status != types.StatusClosed {
			t.Errorf("status after close = %s, want closed", gotClosed.Status)
		}
	})

	t.Run("second_pool_alias_also_claimable", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Night shift item", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "night-crew")
		bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "owl")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "owl" {
			t.Errorf("assignee = %q, want owl", got.Assignee)
		}
	})

	t.Run("real_actor_assignment_still_protected", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Alice's work", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")

		out := bdUpdateFail(t, bd, dir, issue.ID, "--claim", "--actor", "tortoise")
		if !strings.Contains(out, "already assigned to") {
			t.Errorf("expected anti-steal refusal, got: %s", out)
		}

		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "alice" {
			t.Errorf("assignee after refused claim = %q, want alice (unchanged)", got.Assignee)
		}
	})

	t.Run("unconfigured_alias_still_protected", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Other crew's work", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "other-crew")

		out := bdUpdateFail(t, bd, dir, issue.ID, "--claim", "--actor", "tortoise")
		if !strings.Contains(out, "already assigned to") {
			t.Errorf("expected refusal for unconfigured alias, got: %s", out)
		}
	})

	t.Run("pool_claim_stamps_lease", func(t *testing.T) {
		// A pool take is a normal claim: it must carry a lease so the
		// reclaim machinery can recover it if the taker dies.
		issue := bdCreate(t, bd, dir, "Leased pool item", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "fable-crew")
		bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "tortoise")

		raw := bdShowJSON(t, bd, dir, issue.ID)
		if !strings.Contains(raw, "lease_expires_at") {
			t.Errorf("pool claim should stamp a lease; show --json: %s", raw)
		}
	})
}
