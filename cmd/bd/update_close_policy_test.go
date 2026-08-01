//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Generic update close policy belongs to shared lifecycle conformance. These
// command tests retain only command-specific wiring for direct assignee
// transfer and the batch, proxied, embedded, and cross-backend update paths.

// TestUpdateClosePolicyDirectForceStillFencesAssigneeTransfer keeps the other
// half of `--force` intact. Conditioning it on an assignee edit must not turn
// it off when there IS one: a transfer away from a live foreign claim is still
// exactly what the flag authorizes.
func TestUpdateClosePolicyDirectForceStillFencesAssigneeTransfer(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-ucpa", "Held by another actor", func(i *types.Issue) {
		i.Assignee = "someone-else"
		i.Status = types.StatusInProgress
	})

	// Without --force the fence holds.
	env.setFlags(updateCmd, map[string]string{"assignee": "thief"})
	if res := env.run(updateCmd, "test-ucpa"); res.exitCode == 0 {
		t.Fatalf("unforced transfer succeeded; the fence is gone\nstderr:\n%s", res.stderr)
	}
	if got := env.get("test-ucpa").Assignee; got != "someone-else" {
		t.Fatalf("assignee = %q after a refused transfer, want someone-else", got)
	}

	// With it, the transfer is authorized.
	env.setFlags(updateCmd, map[string]string{"assignee": "thief", "force": "true"})
	if res := env.run(updateCmd, "test-ucpa"); res.exitCode != 0 {
		t.Fatalf("forced transfer exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-ucpa").Assignee; got != "thief" {
		t.Errorf("assignee = %q, want thief", got)
	}
}

// TestUpdateClosePolicyBatchCrossesIntoDone drives `bd batch update`, whose
// transaction reaches the same embedded write funnel without going through the
// facade at all.
func TestUpdateClosePolicyBatchCrossesIntoDone(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbc")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tbc-parent", "tbc-child", "tbc-blocker", "tbc-blocked")
	for _, dep := range []*types.Dependency{
		{IssueID: "tbc-child", DependsOnID: "tbc-parent", Type: types.DepParentChild},
		{IssueID: "tbc-blocked", DependsOnID: "tbc-blocker", Type: types.DepBlocks},
	} {
		if err := st.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("seed dependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// An unforced crossing refuses — and because the batch is one transaction,
	// it takes the WHOLE batch down, including the priority edit on a line that
	// had nothing to do with the refusal. That is the documented contract.
	script := "update tbc-blocker priority=0\nupdate tbc-parent status=closed\n"
	err := runBatchScriptInTx(t, ctx, st, script)
	if err == nil {
		t.Fatal("batch update into done with an open child succeeded, want a refusal")
	}
	if !errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Errorf("batch error = %v, want ErrCloseOpenChildren", err)
	}
	rolledBack, getErr := st.GetIssue(ctx, "tbc-blocker")
	if getErr != nil {
		t.Fatalf("GetIssue tbc-blocker: %v", getErr)
	}
	if rolledBack.Priority != 2 {
		t.Errorf("tbc-blocker priority = %d; an unforced refusal must roll back the whole batch", rolledBack.Priority)
	}

	if err := runBatchScriptInTx(t, ctx, st, "update tbc-blocked status=closed\n"); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("batch error = %v, want ErrCloseBlocked", err)
	}

	// force=true overrides both, in the same one transaction.
	forced := "update tbc-parent status=closed force=true\nupdate tbc-blocked status=closed force=true\n"
	if err := runBatchScriptInTx(t, ctx, st, forced); err != nil {
		t.Fatalf("forced batch update into done: %v", err)
	}
	for _, id := range []string{"tbc-parent", "tbc-blocked"} {
		got, err := st.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue %s: %v", id, err)
		}
		if got.Status != types.StatusClosed {
			t.Errorf("%s status = %q, want closed", id, got.Status)
		}
	}
}

// TestUpdateClosePolicyBatchGrammarForceToken pins the batch update grammar's
// spelling of the override, and — the part that matters — pins the allowlist
// that keeps the reserved update-map key from being client-reachable. A script
// asks for force by the grammar's own token; it can never name the transport
// key itself, which is what stops the key from becoming a policy bypass.
func TestUpdateClosePolicyBatchGrammarForceToken(t *testing.T) {
	updates, err := parseUpdateKVs([]string{"status=closed", "force=true"})
	if err != nil {
		t.Fatalf("parseUpdateKVs(force=true): %v", err)
	}
	if got := updates[issueops.OpForceClosePolicy]; got != true {
		t.Errorf("updates[%q] = %v, want true", issueops.OpForceClosePolicy, got)
	}
	if updates["status"] != "closed" {
		t.Errorf("updates[status] = %v, want closed", updates["status"])
	}

	unforced, err := parseUpdateKVs([]string{"status=closed", "force=false"})
	if err != nil {
		t.Fatalf("parseUpdateKVs(force=false): %v", err)
	}
	if got := unforced[issueops.OpForceClosePolicy]; got != false {
		t.Errorf("updates[%q] = %v, want false", issueops.OpForceClosePolicy, got)
	}

	if _, err := parseUpdateKVs([]string{"force=perhaps"}); err == nil {
		t.Error("parseUpdateKVs accepted a non-boolean force value")
	}
	if _, err := parseUpdateKVs([]string{"_force_close_policy=true"}); err == nil {
		t.Error("parseUpdateKVs accepted the reserved update-map key as a client token")
	}
	if _, err := parseUpdateKVs([]string{"description=foo"}); err == nil {
		t.Error("parseUpdateKVs stopped rejecting keys outside its allowlist")
	}
}

// TestUpdateClosePolicyProxiedSpecCarriesForce pins the proxied path's
// translation of `--force`. An earlier attempt was reverted for exactly this
// missing mapping: the proxied caller built a spec that never carried the
// override, so a shared policy check refused the close with no way for the
// user to say otherwise. The spec must carry it, and must not invent it.
func TestUpdateClosePolicyProxiedSpecCarriesForce(t *testing.T) {
	current := &types.Issue{ID: "test-ucpp", Status: types.StatusOpen}

	forced := buildUpdateSpecForIssue(current, &updateInput{
		fields: map[string]any{"status": string(types.StatusClosed)}, force: true,
	})
	if got := forced.Fields[issueops.OpForceClosePolicy]; got != true {
		t.Errorf("spec.Fields[%q] = %v, want true", issueops.OpForceClosePolicy, got)
	}
	if got := forced.Fields["status"]; got != string(types.StatusClosed) {
		t.Errorf("spec.Fields[status] = %v, want closed", got)
	}

	unforced := buildUpdateSpecForIssue(current, &updateInput{
		fields: map[string]any{"status": string(types.StatusClosed)},
	})
	if _, ok := unforced.Fields[issueops.OpForceClosePolicy]; ok {
		t.Errorf("spec.Fields carries %q without --force", issueops.OpForceClosePolicy)
	}
}
