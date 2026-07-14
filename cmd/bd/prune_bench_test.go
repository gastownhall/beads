//go:build cgo

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// mockRefStore satisfies storage.DoltStorage using only SearchIssues and
// GetIssueComments; all other methods panic if called.
type mockRefStore struct {
	storage.DoltStorage
	openBeads []*types.Issue
}

func (m *mockRefStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return m.openBeads, nil
}

func (m *mockRefStore) GetIssueComments(_ context.Context, _ string) ([]*types.Comment, error) {
	return nil, nil
}

// GetCustomStatusesDetailed lets buildReferencedSet enumerate active custom
// statuses; this fixture configures none.
func (m *mockRefStore) GetCustomStatusesDetailed(_ context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

func TestCandidateIDMatcherWordBoundaries(t *testing.T) {
	candidates := map[string]bool{
		"be-ref-001":   true,
		"be-ref-001.1": true,
		"be-ref-002":   true,
	}
	matcher := newCandidateIDMatcher(candidates)

	found := make(map[string]bool)
	matcher.findAll("see (be-ref-001), be-ref-001.1 and xbe-ref-002 but not be-ref-002x", found)

	if !found["be-ref-001"] {
		t.Fatal("expected be-ref-001 to match at punctuation boundaries")
	}
	if !found["be-ref-001.1"] {
		t.Fatal("expected be-ref-001.1 to match at punctuation boundaries")
	}
	if found["be-ref-002"] {
		t.Fatal("did not expect be-ref-002 to match inside word boundaries")
	}
}

// TestPruneLargeFixture asserts that buildReferencedSet completes in <5s on a
// 10K-open-bead × ~5KB-body fixture (NFR-02 from be-5sn).
func TestPruneLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large fixture bench")
	}

	const (
		openBeadCount      = 10_000
		candidateCount     = 100
		seededRefCount     = 20
		bodyPadding        = 5_000 // bytes of filler per bead
		maxDurationSeconds = 5
	)

	// Build 100 closed candidate IDs (be-ref-000 … be-ref-099).
	candidates := make(map[string]bool, candidateCount)
	for i := 0; i < candidateCount; i++ {
		candidates[fmt.Sprintf("be-ref-%03d", i)] = true
	}

	// Build the seeded ID set (first 20 candidates).
	seeded := make([]string, seededRefCount)
	i := 0
	for id := range candidates {
		if i >= seededRefCount {
			break
		}
		seeded[i] = id
		i++
	}

	// Padding text that does NOT contain any candidate ID substring.
	pad := strings.Repeat("x", bodyPadding)

	// Create 10K open beads. Every 500th bead gets one seeded ID planted in
	// its description so that exactly seededRefCount beads carry references.
	openBeads := make([]*types.Issue, openBeadCount)
	seededIdx := 0
	for j := 0; j < openBeadCount; j++ {
		desc := pad
		if seededIdx < seededRefCount && j%500 == 0 {
			desc = fmt.Sprintf("%s %s %s", pad[:100], seeded[seededIdx], pad[100:])
			seededIdx++
		}
		openBeads[j] = &types.Issue{
			ID:          fmt.Sprintf("be-open-%05d", j),
			Status:      types.StatusOpen,
			Description: desc,
		}
	}

	st := &mockRefStore{openBeads: openBeads}
	ctx := context.Background()

	start := time.Now()
	refSet, err := buildReferencedSet(ctx, st, candidates)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("buildReferencedSet error: %v", err)
	}

	// Performance assertion (NFR-02). The race detector adds multi-x overhead
	// that invalidates a wall-clock budget (buildReferencedSet measures ~4x
	// slower under -race), so only enforce the bound in non-race builds. The
	// correctness assertions below still run under -race, which is where the
	// large-fixture pass earns its keep as a data-race check.
	if !raceEnabled && elapsed > maxDurationSeconds*time.Second {
		t.Errorf("buildReferencedSet took %v; must complete in <%ds on 10K-bead fixture", elapsed, maxDurationSeconds)
	}

	// Correctness assertion: exactly the seeded IDs should be in the reference set.
	if len(refSet) != seededIdx {
		t.Errorf("expected %d referenced IDs, got %d", seededIdx, len(refSet))
	}
	for _, id := range seeded[:seededIdx] {
		if !refSet[id] {
			t.Errorf("expected %s to be in refSet but it was not", id)
		}
	}
}
