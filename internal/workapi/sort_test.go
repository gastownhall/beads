package workapi

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestReadyFilterFromIssueFilterDeferredIsRestrictive(t *testing.T) {
	deferred := ReadyFilterFromIssueFilter(types.IssueFilter{Deferred: true})

	if !deferred.Deferred {
		t.Fatal("Deferred list filter must remain restrictive in the ready filter")
	}
	if !deferred.IncludeDeferred {
		t.Fatal("Deferred ready filter must admit future-deferred issues")
	}

	ordinary := ReadyFilterFromIssueFilter(types.IssueFilter{})
	if ordinary.Deferred || ordinary.IncludeDeferred {
		t.Fatalf("ordinary ready filter unexpectedly changed: %#v", ordinary)
	}
}
