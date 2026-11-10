//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedSquashWispMoleculeClearsRootEphemeral mirrors
// TestSquashWispMoleculeClearsRootEphemeral but runs against the embedded
// Dolt backend used by CI. Without this, the standalone-Dolt regression test
// skips in the Embedded Dolt CI tier and the fix's lines stay uncovered.
func TestEmbeddedSquashWispMoleculeClearsRootEphemeral(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	ctx := context.Background()
	beadsDir := t.TempDir()

	s, err := embeddeddolt.Open(ctx, beadsDir, "moltest", "main")
	if err != nil {
		t.Fatalf("embeddeddolt.Open: %v", err)
	}
	defer s.Close()

	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	root := &types.Issue{
		Title:     "Wisp Molecule Root",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		Ephemeral: true,
	}
	if err := s.CreateIssue(ctx, root, "test"); err != nil {
		t.Fatalf("CreateIssue root: %v", err)
	}

	child := &types.Issue{
		Title:     "Wisp Step",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("CreateIssue child: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	result, err := squashMolecule(ctx, s, root, []*types.Issue{child}, false, "", "test")
	if err != nil {
		t.Fatalf("squashMolecule: %v", err)
	}
	if !result.WispSquash {
		t.Error("expected WispSquash=true for ephemeral root")
	}

	closed, err := s.GetIssue(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetIssue root after squash: %v", err)
	}
	if closed.Status != types.StatusClosed {
		t.Errorf("root status = %v, want closed", closed.Status)
	}
	if closed.Ephemeral {
		t.Error("root Ephemeral should be false after squash; closed wisp roots otherwise leak as duplicate JSONL rows on every export cycle")
	}
}

// squashShipFormula and squashRollbackFormula are the main flow and the
// conditional arm of TestEmbeddedMolSquashKeepsTheStepsAConditionalFallbackWaitsOn.
const squashShipFormula = `formula = "ship"
description = "ship it"
version = 1
type = "workflow"

[[steps]]
id = "deploy"
title = "Deploy"
description = "deploy"

[[steps]]
id = "verify"
title = "Verify"
needs = ["deploy"]
description = "verify"

[[steps]]
id = "announce"
title = "Announce"
needs = ["verify"]
description = "announce"
`

const squashRollbackFormula = `formula = "rollback"
description = "roll back"
version = 1
type = "workflow"

[[steps]]
id = "revert"
title = "Revert"
description = "revert"

[[steps]]
id = "notify"
title = "Notify"
needs = ["revert"]
description = "notify"
`

// TestEmbeddedMolSquashKeepsTheStepsAConditionalFallbackWaitsOn is GH#5577 end
// to end on bd mol squash, the cleanup bd suggests when a molecule completes.
// A rollback arm bonded onto the deploy step (bd mol bond --type conditional)
// and a durable fallback with a conditional-blocks dependency on the verify
// step wait on steps that closed with a success reason. Deleting either step
// would drop its edge and put the fallback in bd ready. The squash keeps both
// steps and deletes the third, its dry run says so, and once the fallbacks
// are disposed of, wisp gc collects the kept steps.
func TestEmbeddedMolSquashKeepsTheStepsAConditionalFallbackWaitsOn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sqk")
	formulas := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulas, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	for name, body := range map[string]string{"ship": squashShipFormula, "rollback": squashRollbackFormula} {
		if err := os.WriteFile(filepath.Join(formulas, name+".formula.toml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write formula %s: %v", name, err)
		}
	}
	decode := func(command, out string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("parse %s JSON: %v\n%s", command, err, out)
		}
	}

	var wisp struct {
		NewEpicID string            `json:"new_epic_id"`
		IDMapping map[string]string `json:"id_mapping"`
	}
	decode("bd mol wisp", bdCommand(t, bd, dir, "mol", "wisp", "ship", "--json"), &wisp)
	root := wisp.NewEpicID
	deploy, verify, announce := wisp.IDMapping["ship.deploy"], wisp.IDMapping["ship.verify"], wisp.IDMapping["ship.announce"]

	var bond struct {
		IDMapping map[string]string `json:"id_mapping"`
	}
	decode("bd mol bond", bdCommand(t, bd, dir, "mol", "bond", "rollback", deploy, "--type", "conditional", "--json"), &bond)
	arm, revert, notify := bond.IDMapping["rollback"], bond.IDMapping["rollback.revert"], bond.IDMapping["rollback.notify"]
	if root == "" || deploy == "" || verify == "" || announce == "" || arm == "" || revert == "" || notify == "" {
		t.Fatalf("missing ids: wisp %v, bond %v", wisp.IDMapping, bond.IDMapping)
	}
	restore := bdCreate(t, bd, dir, "Restore the release notes").ID
	bdDepAdd(t, bd, dir, restore, verify, "--type", "conditional-blocks")

	bdClose(t, bd, dir, deploy, "--reason", "deployed ok")
	bdClose(t, bd, dir, verify, "--reason", "verified")
	bdClose(t, bd, dir, announce, "--reason", "announced")

	assertFallbacksHeld := func(phase string) {
		t.Helper()
		ready := wakeReadyIDs(t, bd, dir, "--include-ephemeral")
		for _, id := range []string{arm, revert, notify, restore} {
			if ready[id] {
				t.Fatalf("%s put fallback %s in bd ready after the step it waits on closed with a success reason (GH#5577)", phase, id)
			}
		}
	}
	assertFallbacksHeld("closing the molecule's steps")

	if out := bdCommand(t, bd, dir, "mol", "squash", root, "--dry-run"); !strings.Contains(out, "Kept: 2 closed wisp(s)") {
		t.Errorf("bd mol squash --dry-run does not say it keeps %s and %s:\n%s", deploy, verify, out)
	}

	var squash struct {
		DeletedCount int      `json:"deleted_count"`
		KeptIDs      []string `json:"kept_ids"`
	}
	decode("bd mol squash", bdCommand(t, bd, dir, "mol", "squash", root, "--json"), &squash)
	want := []string{deploy, verify}
	sort.Strings(want)
	sort.Strings(squash.KeptIDs)
	if !reflect.DeepEqual(squash.KeptIDs, want) || squash.DeletedCount != 1 {
		t.Errorf("bd mol squash kept_ids=%v deleted_count=%d, want kept_ids=%v and only %s deleted", squash.KeptIDs, squash.DeletedCount, want, announce)
	}
	for _, id := range want {
		if !bdShowSucceeds(t, bd, dir, id) {
			t.Fatalf("bd mol squash deleted %s, which a conditional-blocks fallback still waits on", id)
		}
	}
	bdShowFail(t, bd, dir, announce)
	assertFallbacksHeld("bd mol squash")

	// Disposing of the unused fallbacks lets the kept steps go.
	bdClose(t, bd, dir, revert, notify, "--reason", "skipped: deploy succeeded")
	bdClose(t, bd, dir, arm, "--reason", "skipped: deploy succeeded")
	bdClose(t, bd, dir, restore, "--reason", "skipped: verify passed")
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	bdShowFail(t, bd, dir, deploy)
	bdShowFail(t, bd, dir, verify)
}
