//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestRoutedResolutionLooksPastLocalNearMiss pins the local tier's half of the
// routing order: the local store is asked first, and only its not-found lets a
// routes.jsonl prefix route answer. An id that names nothing locally must
// therefore come back not-found there even when a local bead shares a fragment
// with it; otherwise the local "match" silently answers for the routed issue,
// and a mutating command acts on the wrong bead.
//
// The setup targets the matcher #4376's first revision proposed: hr-8wnq lives
// in the routed rig, and the local store holds bd-8wnq2, whose hash starts with
// the text after hr-8wnq's last dash. A matcher that compares only that last
// segment claims hr-8wnq abbreviates bd-8wnq2. Partial matching is
// leading-prefix over the whole hash part (#4939), so the local tier must
// report not-found and the hr- route must win.
//
// Both directions are pinned, because the routed assertion alone also passes
// when the local bead is unreachable: a genuine abbreviation of the local bead
// must still resolve locally, without routing.
//
// NOTE: assigns the dbPath global and uses os.Chdir, so it cannot run in
// parallel (same constraint as
// TestResolveAndGetIssueForMutationExactSurfacesRoutedAbbreviationRefusal).
func TestRoutedResolutionLooksPastLocalNearMiss(t *testing.T) {
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

	const routedID = "hr-8wnq"
	// Neither hr-8wnq nor an abbreviation of it, but its hash starts with the
	// text after hr-8wnq's last dash.
	const localNearMiss = "bd-8wnq2"
	// A genuine leading-prefix abbreviation of localNearMiss.
	const localAbbrev = "8wnq"

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "bd")
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID:        localNearMiss,
		Title:     "Local near miss",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create local issue %s: %v", localNearMiss, err)
	}

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "hr")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID:        routedID,
		Title:     "Routed target",
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
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"hr-","path":"rig"}`), 0o644); err != nil {
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

	// Reads (bd show) and mutations (bd close, bd update) share the local
	// tier; only the routed store's open mode differs.
	resolvers := []struct {
		name    string
		resolve func(context.Context, storage.DoltStorage, string) (*RoutedResult, error)
	}{
		{"read", resolveAndGetIssueWithRouting},
		{"mutation", resolveAndGetIssueForMutation},
	}
	for _, r := range resolvers {
		// Direction 1: the routed issue must be what comes back, from the
		// routed rig.
		result, err := r.resolve(ctx, townStore, routedID)
		if err != nil {
			t.Errorf("%s: resolve %q: %v, want %s from the hr- route", r.name, routedID, err, routedID)
		} else {
			if result.ResolvedID != routedID || !result.Routed {
				t.Errorf("%s: %q resolved to %s (routed=%v), want %s from the hr- route\n"+
					"a local near miss answered for the routed issue, so routing never ran",
					r.name, routedID, result.ResolvedID, result.Routed, routedID)
			}
			if result.Issue == nil {
				t.Errorf("%s: %q returned no issue, want %s", r.name, routedID, routedID)
			} else if result.Issue.ID != routedID {
				t.Errorf("%s: %q returned issue %s, want %s", r.name, routedID, result.Issue.ID, routedID)
			}
			result.Close()
		}

		// Direction 2: the local bead is reachable, so direction 1 is not
		// passing merely because the local store answers nothing.
		result, err = r.resolve(ctx, townStore, localAbbrev)
		if err != nil {
			t.Errorf("%s: resolve %q: %v, want local %s", r.name, localAbbrev, err, localNearMiss)
			continue
		}
		if result.ResolvedID != localNearMiss || result.Routed {
			t.Errorf("%s: %q resolved to %s (routed=%v), want local %s",
				r.name, localAbbrev, result.ResolvedID, result.Routed, localNearMiss)
		}
		result.Close()
	}
}
