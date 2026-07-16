package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// advisoryEvents reads ownership_advisory events for an issue, returning the
// per-event class recorded in new_value.
func advisoryEvents(t *testing.T, ctx context.Context, store *DoltStore, id string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		`SELECT new_value FROM events WHERE issue_id = ? AND event_type = ? ORDER BY id`,
		id, string(issueops.EventOwnershipAdvisory))
	if err != nil {
		t.Fatalf("query advisory events: %v", err)
	}
	defer rows.Close()
	var classes []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		classes = append(classes, v)
	}
	return classes
}

func seedClaimedWithToken(t *testing.T, ctx context.Context, store *DoltStore, id, owner, token string) {
	t.Helper()
	seedOpenIssue(t, ctx, store, id)
	if err := store.ClaimIssue(issueops.WithHolderToken(ctx, token), id, owner); err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
}

// TestAdvisoryOffRecordsNothing: with enforcement off (default), a mutation by
// a mismatched token lands and emits no advisory event.
func TestAdvisoryOffRecordsNothing(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedWithToken(t, ctx, store, "adv-off", "alice", "tok-1")
	// A zombie (same actor, wrong token) writes; off mode allows and is silent.
	zctx := issueops.WithHolderToken(ctx, "tok-stale")
	if err := store.UpdateIssue(zctx, "adv-off", map[string]interface{}{"notes": "zombie write"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if ev := advisoryEvents(t, ctx, store, "adv-off"); len(ev) != 0 {
		t.Errorf("off mode emitted advisory events: %v", ev)
	}
}

// TestAdvisoryClassifiesTokenMismatch: the real zombie signal — same actor as
// the assignee, but a different (non-empty) holder token — is classified
// token_mismatch, and the write still lands (advisory does not block).
func TestAdvisoryClassifiesTokenMismatch(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-zombie", "alice", "tok-live")

	zctx := issueops.WithHolderToken(ctx, "tok-stale")
	if err := store.UpdateIssue(zctx, "adv-zombie", map[string]interface{}{"notes": "stale write"}, "alice"); err != nil {
		t.Fatalf("update (advisory must not block): %v", err)
	}
	got, err := store.GetIssue(ctx, "adv-zombie")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Notes != "stale write" {
		t.Errorf("advisory blocked the write: notes=%q", got.Notes)
	}
	ev := advisoryEvents(t, ctx, store, "adv-zombie")
	if len(ev) != 1 || ev[0] != string(issueops.AdvisoryTokenMismatch) {
		t.Errorf("advisory classes = %v, want [%s]", ev, issueops.AdvisoryTokenMismatch)
	}
}

// TestAdvisoryClassifiesCrossActor: an orchestrator/infra write (actor !=
// assignee) is classified cross_actor_infra — the noise class the require
// flip must see converted to guarded verbs.
func TestAdvisoryClassifiesCrossActor(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-infra", "alice", "tok-live")

	// controller writes metadata cross-actor with no holder token.
	if err := store.UpdateIssue(ctx, "adv-infra", map[string]interface{}{"metadata": `{"gc.outcome":"pass"}`}, "controller"); err != nil {
		t.Fatalf("update: %v", err)
	}
	ev := advisoryEvents(t, ctx, store, "adv-infra")
	if len(ev) != 1 || ev[0] != string(issueops.AdvisoryCrossActor) {
		t.Errorf("advisory classes = %v, want [%s]", ev, issueops.AdvisoryCrossActor)
	}
}

// TestAdvisoryClassifiesEmptyTokenLegacy: a row claimed without a token
// (legacy/pre-token) mutated by a token-bearing caller is empty_token_legacy,
// distinct from a genuine mismatch.
func TestAdvisoryClassifiesEmptyTokenLegacy(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	// Claim without a holder token (legacy row).
	seedOpenIssue(t, ctx, store, "adv-legacy")
	if err := store.ClaimIssue(ctx, "adv-legacy", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	tctx := issueops.WithHolderToken(ctx, "tok-new")
	if err := store.UpdateIssue(tctx, "adv-legacy", map[string]interface{}{"notes": "n"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}
	ev := advisoryEvents(t, ctx, store, "adv-legacy")
	if len(ev) != 1 || ev[0] != string(issueops.AdvisoryEmptyTokenLegacy) {
		t.Errorf("advisory classes = %v, want [%s]", ev, issueops.AdvisoryEmptyTokenLegacy)
	}
}

// TestAdvisoryCleanHolderNoEvent: the current holder (matching actor AND
// token) writing its own bead emits nothing.
func TestAdvisoryCleanHolderNoEvent(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-clean", "alice", "tok-live")

	hctx := issueops.WithHolderToken(ctx, "tok-live")
	if err := store.UpdateIssue(hctx, "adv-clean", map[string]interface{}{"notes": "mine"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if ev := advisoryEvents(t, ctx, store, "adv-clean"); len(ev) != 0 {
		t.Errorf("clean holder write emitted advisory events: %v", ev)
	}
}

// TestAdvisoryClose: a guarded-less close by a zombie under advisory lands but
// records the mismatch (close is an in-place mutation of a claimed row).
func TestAdvisoryClose(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-close", "alice", "tok-live")

	zctx := issueops.WithHolderToken(ctx, "tok-stale")
	if err := store.CloseIssue(zctx, "adv-close", "zombie done", "alice", ""); err != nil {
		t.Fatalf("close (advisory must not block): %v", err)
	}
	ev := advisoryEvents(t, ctx, store, "adv-close")
	if len(ev) != 1 || ev[0] != string(issueops.AdvisoryTokenMismatch) {
		t.Errorf("advisory classes = %v, want [%s]", ev, issueops.AdvisoryTokenMismatch)
	}
}

// TestAdvisoryTokenlessCaller: a tokenless caller (a human via bd, or a tool
// without BEADS_HOLDER_TOKEN) writing a token-bearing claim is classified
// tokenless_caller, NOT token_mismatch — a stale incarnation carries its own
// token, so an empty caller token is not the zombie signal.
func TestAdvisoryTokenlessCaller(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-tokenless", "alice", "tok-live")

	// alice (the assignee) writes with NO holder token.
	if err := store.UpdateIssue(ctx, "adv-tokenless", map[string]interface{}{"notes": "human edit"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}
	ev := advisoryEvents(t, ctx, store, "adv-tokenless")
	if len(ev) != 1 || ev[0] != string(issueops.AdvisoryTokenlessCaller) {
		t.Errorf("advisory classes = %v, want [%s]", ev, issueops.AdvisoryTokenlessCaller)
	}
}

// TestAdvisoryTokenNeverInEvents: the advisory event must not persist the
// holder token anywhere readable — events.old_value is exposed via bd history
// / the event bus, so leaking the token there would defeat the never-surfaced
// invariant (D12) as surely as putting it in the issue read surface would.
func TestAdvisoryTokenNeverInEvents(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.EnforcementConfigKey, "advisory"); err != nil {
		t.Fatalf("set advisory: %v", err)
	}
	seedClaimedWithToken(t, ctx, store, "adv-leak", "alice", "super-secret-token")

	// Trigger a token_mismatch advisory event (row token non-empty).
	if err := store.UpdateIssue(issueops.WithHolderToken(ctx, "other-token"), "adv-leak",
		map[string]interface{}{"notes": "n"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}

	rows, err := store.db.QueryContext(ctx,
		`SELECT old_value, new_value FROM events WHERE issue_id = ?`, "adv-leak")
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oldV, newV string
		if err := rows.Scan(&oldV, &newV); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(oldV, "super-secret-token") || strings.Contains(newV, "super-secret-token") ||
			strings.Contains(oldV, "other-token") || strings.Contains(newV, "other-token") {
			t.Errorf("holder token leaked into an event: old=%q new=%q", oldV, newV)
		}
	}
}

// TestReclaimRefreshesHolderToken: a fresh incarnation (same actor name, new
// token) re-claiming its own in_progress work through the idempotent path
// takes over the token — so the live incarnation classifies clean and the
// stale one would classify as the zombie, not the inverse.
func TestReclaimRefreshesHolderToken(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedWithToken(t, ctx, store, "adv-refresh", "alice", "tok-incarnation-1")
	// Same actor, fresh incarnation token, re-claims the in_progress row.
	if err := store.ClaimIssue(issueops.WithHolderToken(ctx, "tok-incarnation-2"), "adv-refresh", "alice"); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "adv-refresh"); got != "tok-incarnation-2" {
		t.Errorf("holder_token after re-claim = %q, want tok-incarnation-2 (refreshed)", got)
	}

	// A tokenless re-claim must NOT wipe the live token.
	if err := store.ClaimIssue(ctx, "adv-refresh", "alice"); err != nil {
		t.Fatalf("tokenless re-claim: %v", err)
	}
	if got := readHolderToken(t, ctx, store, "issues", "adv-refresh"); got != "tok-incarnation-2" {
		t.Errorf("tokenless re-claim wiped the token: got %q, want tok-incarnation-2", got)
	}
}
