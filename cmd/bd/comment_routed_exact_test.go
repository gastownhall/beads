//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// TestResolveAndGetIssueForMutationExactSurfacesRoutedAbbreviationRefusal pins
// the error SELECTION in resolveAndGetIssueForMutationExact, the one part of
// the exact-match contract the rest of this PR's tests cannot reach: they all
// exercise the LOCAL tier, where the refusal is the function's own return
// value. A cross-rig or auto-routed tier mints the same sentinel, and the
// truthful message the comment write paths print (comment.go, comments.go)
// depends on errors.Is seeing it from there too.
//
// The setup is the shape the sentinel exists for: the local store genuinely
// knows nothing about the id, so its error is the plain "no issue found
// matching" — which is FALSE here, because prefix routing finds the
// abbreviation's real target in the other rig and refuses it there.
//
// Both directions are pinned, because either assertion alone passes for the
// wrong reason: an abbreviation of a routed issue must select the ROUTED
// refusal over the local not-found, and an id that names nothing anywhere
// must still come back as a plain not-found rather than claiming an
// abbreviation exists.
//
// NOTE: assigns the dbPath global and uses os.Chdir, so it cannot run in
// parallel (same constraint as TestCheckBeadGateCrossRigPrefixRoute).
func TestResolveAndGetIssueForMutationExactSurfacesRoutedAbbreviationRefusal(t *testing.T) {
	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, "rig", ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	// The town store is the local store the comment write paths hold; it never
	// holds the routed id, so it supplies the plain not-found this test is
	// about discarding.
	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	const routedID = "gt-abcdefgh"
	// One character short of routedID: not an exact id anywhere, but a valid
	// leading-prefix abbreviation of a real issue in the routed rig.
	const routedAbbrev = "gt-abcdefg"
	// Routes to the same rig, but names nothing there in either form.
	const absentRoutedID = "gt-zzzzzzzz"

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID:        routedID,
		Title:     "Routed comment target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create routed issue %s: %v", routedID, err)
	}
	// Prefix routing opens the target itself, so hand the database back first.
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("change to town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Direction 1: the routed tier's refusal must reach the caller, because
	// the issue it refuses demonstrably exists.
	result, err := resolveAndGetIssueForMutationExact(ctx, townStore, routedAbbrev)
	if result != nil {
		result.Close()
		t.Fatalf("abbreviation %q of routed issue %s resolved instead of being refused", routedAbbrev, routedID)
	}
	if err == nil {
		t.Fatalf("abbreviation %q of routed issue %s returned no error", routedAbbrev, routedID)
	}
	if !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf("routed abbreviation %q error = %v, want one satisfying errors.Is(err, utils.ErrAbbreviatedIDNotAllowed)\n"+
			"the comment write paths key their truthful message off this sentinel, so a local not-found here prints "+
			"\"no issue found matching\" for an issue the routed rig holds", routedAbbrev, err)
	}

	// Direction 2: an id that names nothing in either store must NOT be
	// reported as an abbreviation refusal — the selection has to be driven by
	// the routed error's own sentinel, not by having routed at all.
	result, err = resolveAndGetIssueForMutationExact(ctx, townStore, absentRoutedID)
	if result != nil {
		result.Close()
		t.Fatalf("absent routed id %q resolved unexpectedly", absentRoutedID)
	}
	if err == nil {
		t.Fatalf("absent routed id %q returned no error", absentRoutedID)
	}
	if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf("absent routed id %q error = %v, want NOT an abbreviation refusal", absentRoutedID, err)
	}
	if !isNotFoundErr(err) {
		t.Errorf("absent routed id %q error = %v, want a not-found error", absentRoutedID, err)
	}
}
