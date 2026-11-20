package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestOpenSupersedeDependentsSkipsClosedPinnedReplacementAndDuplicates(t *testing.T) {
	dep := func(id string, status types.Status) *types.IssueWithDependencyMetadata {
		return &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: id, Status: status},
			DependencyType: types.DepRelated,
		}
	}
	got := openSupersedeDependents([]*types.IssueWithDependencyMetadata{
		dep("bd-open", types.StatusOpen),
		dep("bd-closed", types.StatusClosed),
		nil,
		dep("bd-new", types.StatusOpen),
		dep("bd-claimed", types.StatusInProgress),
		dep("bd-pinned", types.StatusPinned),
		dep("bd-open", types.StatusOpen),
		dep("bd-deferred", types.StatusDeferred),
	}, "bd-new")
	want := []string{"bd-open", "bd-claimed", "bd-deferred"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("openSupersedeDependents = %v, want %v", got, want)
	}
}

func TestSupersedeAckDescriptionShowsBothVersionsAndBlockedIssues(t *testing.T) {
	got := supersedeAckDescription(
		&types.Issue{ID: "bd-old", Title: "Use X", Description: "X because cheap"},
		&types.Issue{ID: "bd-new", Title: "Use Y", Description: "Y because safe"},
		[]string{"bd-a", "bd-b"},
	)
	for _, want := range []string{"bd-old was superseded by bd-new", "bd-a, bd-b", "Before: bd-old Use X", "X because cheap", "After: bd-new Use Y", "Y because safe"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
}
