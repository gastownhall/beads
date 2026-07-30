//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdConfigSet runs `bd config set` and fails the test if the key is not
// recognized — the exit code alone misses that, and an unrecognized key would
// make the whole feature warn at every adopter's setup step.
func bdConfigSet(t *testing.T, bd, dir, key, value string) {
	t.Helper()
	cmd := exec.Command(bd, "config", "set", key, value)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd config set %s %s failed: %v\n%s", key, value, err, out)
	}
	if strings.Contains(string(out), "not a recognized config key") {
		t.Fatalf("bd config set %s warned about an unrecognized key:\n%s", key, out)
	}
}

// TestEmbeddedLeaseDisarm drives `bd lease disarm` end to end: the one-shot
// flip reports what it cleared, releases nothing, survives as store config so
// later claims are unleased, and is idempotent on a second run.
func TestEmbeddedLeaseDisarm(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ld")

	issue := bdCreate(t, bd, dir, "Held across the disarm", "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "alice")
	claimed := bdShow(t, bd, dir, issue.ID)
	if claimed.LeaseExpiresAt == nil {
		t.Fatal("test setup: a default (lease.auto on) claim did not stamp a lease")
	}

	out, code := bdRunRaw(t, bd, dir, nil, "lease", "disarm")
	if code != 0 {
		t.Fatalf("bd lease disarm exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "cleared 1 lease") {
		t.Errorf("bd lease disarm should report the lease it cleared, got:\n%s", out)
	}

	// Nothing released: the claim, its holder and its fence all stand.
	after := bdShow(t, bd, dir, issue.ID)
	if after.Status != types.StatusInProgress || after.Assignee != "alice" {
		t.Errorf("disarm released the claim: status=%s assignee=%q", after.Status, after.Assignee)
	}
	if after.ClaimFence != claimed.ClaimFence {
		t.Errorf("disarm moved claim_fence %d → %d; disarming is not an ownership transition",
			claimed.ClaimFence, after.ClaimFence)
	}
	if after.LeaseExpiresAt != nil {
		t.Error("disarm left the lease armed")
	}

	// The flip persisted: a later claim carries no lease, and heartbeating it
	// is refused rather than silently re-arming.
	next := bdCreate(t, bd, dir, "Claimed after the disarm", "--type", "task")
	bdUpdate(t, bd, dir, next.ID, "--claim", "--actor", "bob")
	if got := bdShow(t, bd, dir, next.ID); got.LeaseExpiresAt != nil {
		t.Error("post-disarm claim stamped a lease")
	}
	hbOut, hbCode := bdRunRaw(t, bd, dir, nil, "heartbeat", next.ID, "--actor", "bob")
	if hbCode == 0 {
		t.Errorf("heartbeat on an unleased claim should fail, got:\n%s", hbOut)
	}
	if !strings.Contains(hbOut, "no lease") {
		t.Errorf("heartbeat refusal should name the missing lease, got:\n%s", hbOut)
	}

	// Idempotent: a second run flips an already-off key and sweeps nothing.
	againOut, againCode := bdRunRaw(t, bd, dir, nil, "lease", "disarm", "--json")
	if againCode != 0 {
		t.Fatalf("second bd lease disarm exit = %d, want 0\n%s", againCode, againOut)
	}
	var payload struct {
		LeaseAuto string `json:"lease_auto"`
		Disarmed  int64  `json:"disarmed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(againOut)), &payload); err != nil {
		t.Fatalf("bd lease disarm --json emitted unparseable output %q: %v", againOut, err)
	}
	if payload.LeaseAuto != "off" || payload.Disarmed != 0 {
		t.Errorf("second disarm reported %+v, want lease_auto=off disarmed=0", payload)
	}
}

// TestEmbeddedLeaseAutoConfigRoundTrip: the config key is the documented way
// to reach the same state without the sweep, so `bd config set lease.auto
// false` must be accepted as a recognized key and honored by the next claim.
func TestEmbeddedLeaseAutoConfigRoundTrip(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lc")

	bdConfigSet(t, bd, dir, "lease.auto", "false")

	issue := bdCreate(t, bd, dir, "Unleased by config", "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "alice")
	got := bdShow(t, bd, dir, issue.ID)
	if got.Status != types.StatusInProgress || got.Assignee != "alice" {
		t.Fatalf("claim did not take: status=%s assignee=%q", got.Status, got.Assignee)
	}
	if got.LeaseExpiresAt != nil {
		t.Error("claim under lease.auto=false stamped a lease")
	}
	if got.ClaimFence == 0 {
		t.Error("claim under lease.auto=false did not bump claim_fence; the claim is still an ownership transition")
	}

	// Re-arming through the same key restores the shipped behavior.
	bdConfigSet(t, bd, dir, "lease.auto", "on")
	rearmed := bdCreate(t, bd, dir, "Leased again", "--type", "task")
	bdUpdate(t, bd, dir, rearmed.ID, "--claim", "--actor", "alice")
	if bdShow(t, bd, dir, rearmed.ID).LeaseExpiresAt == nil {
		t.Error("claim after re-arming lease.auto did not stamp a lease")
	}
}
