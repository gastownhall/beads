package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// planeIssueUC answers the two id-set reads proxiedGateCandidates makes and
// nothing else, each with only the rows its own table holds: GetIssuesByIDs
// the issues, GetWispsByIDs the wisps, as the domain use case does.
type planeIssueUC struct {
	domain.IssueUseCase // any other method is a nil call: the hydrator must not reach one
	issues, wisps       []*types.Issue
	wispsErr            error
}

func (s planeIssueUC) GetIssuesByIDs(context.Context, []string) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s planeIssueUC) GetWispsByIDs(context.Context, []string) ([]*types.Issue, error) {
	if s.wispsErr != nil {
		return nil, s.wispsErr
	}
	return s.wisps, nil
}

// TestProxiedGatedIssueIDsHydratesBothPlanes pins the proxied hydrator to both
// tables. The use case's GetIssuesByIDs reads the issues table only, so a wisp
// gate — the kind `bd mol wisp` clones from a formula — once hydrated to
// nothing, and its step listed undecorated while `bd show` called it GATED.
//
// The unit of work is lookupOnlyUOW: the dependency rows are preloaded, so a
// call to its nil DependencyUseCase would be a panic, not a silent pass.
func TestProxiedGatedIssueIDsHydratesBothPlanes(t *testing.T) {
	step := &types.Issue{ID: "bd-step", Status: types.StatusOpen}
	wispStep := &types.Issue{ID: "bd-wisp-step", Status: types.StatusOpen, Ephemeral: true}
	gate := openGate("bd-gate", "human", "Need design review", "")
	wispGate := openGate("bd-wisp-gate", "", "", "")
	wispGate.Ephemeral = true
	deps := map[string][]*types.Dependency{
		step.ID:     {{IssueID: step.ID, DependsOnID: gate.ID, Type: types.DepBlocks}},
		wispStep.ID: {{IssueID: wispStep.ID, DependsOnID: wispGate.ID, Type: types.DepBlocks}},
	}
	page := []*types.Issue{step, wispStep}

	t.Run("each_plane_decorates_its_own_rows", func(t *testing.T) {
		uw := lookupOnlyUOW{issues: planeIssueUC{
			issues: []*types.Issue{gate},
			wisps:  []*types.Issue{wispGate},
		}}
		got := proxiedGatedIssueIDs(context.Background(), uw, page, deps)
		want := map[string][]string{step.ID: {gate.ID}, wispStep.ID: {wispGate.ID}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("gated = %v, want %v", got, want)
		}
	})

	// A failed wisps read costs the wisp gates their decoration, never the
	// durable gates the issues read already found.
	t.Run("failed_wisps_read_keeps_the_durable_decoration", func(t *testing.T) {
		uw := lookupOnlyUOW{issues: planeIssueUC{
			issues:   []*types.Issue{gate},
			wispsErr: errors.New("wisps read failed"),
		}}
		got := proxiedGatedIssueIDs(context.Background(), uw, page, deps)
		want := map[string][]string{step.ID: {gate.ID}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("gated = %v, want %v", got, want)
		}
	})
}
