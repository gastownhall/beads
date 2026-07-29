//go:build cgo

package main

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestSpawnMolecule_PreservesStepLabels verifies that a cooked formula with
// per-step labels results in spawned issues whose labels are persisted to the
// database. Regression for labels being silently dropped by cloneSubgraph in
// the same shape as the metadata bug fixed by gastownhall/beads#3341.
func TestSpawnMolecule_PreservesStepLabels(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	ctx := t.Context()
	s, err := embeddeddolt.Open(ctx, t.TempDir(), "beads", "main")
	if err != nil {
		t.Fatalf("embeddeddolt.Open failed: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	f := &formula.Formula{
		Formula: "label-test",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:     "work",
				Title:  "Do the work",
				Labels: []string{"worker", "phase:build"},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "label-test")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	result, err := spawnMolecule(ctx, s, subgraph, nil, "", "test", false, types.IDPrefixMol)
	if err != nil {
		t.Fatalf("spawnMolecule failed: %v", err)
	}

	newWorkID, ok := result.IDMapping["label-test.work"]
	if !ok {
		t.Fatalf("result.IDMapping missing entry for label-test.work; got %v", result.IDMapping)
	}
	labels, err := s.GetLabels(ctx, newWorkID)
	if err != nil {
		t.Fatalf("GetLabels(%s) failed: %v", newWorkID, err)
	}
	got := make(map[string]bool, len(labels))
	for _, l := range labels {
		got[l] = true
	}
	for _, want := range []string{"worker", "phase:build"} {
		if !got[want] {
			t.Errorf("spawned issue %s missing label %q; got %v", newWorkID, want, labels)
		}
	}
}
