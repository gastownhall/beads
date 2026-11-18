//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestAdaptiveIDLengthCountsPromotedWisps: issues and wisps share one ID
// space, so promoted `<prefix>-wisp-*` rows sitting in `issues` must count
// toward the hash length of new wisp IDs (bd-u9cfx0, PR #6968).
func TestAdaptiveIDLengthCountsPromotedWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "kz")

	// At 0.001, 12 rows cross the 3→4 length boundary.
	te.exec(t, ctx, "REPLACE INTO config (`key`, value) VALUES ('max_collision_prob', '0.001')")

	// 12 promoted wisps: wisp-namespace IDs that live in the issues table.
	for i := 0; i < 12; i++ {
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: fmt.Sprintf("kz-wisp-p%02d", i), Title: "promoted", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create promoted wisp %d: %v", i, err)
		}
	}
	var inIssues int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'kz-wisp-%'", nil, &inIssues)
	if inIssues != 12 {
		t.Fatalf("issues rows in kz-wisp namespace = %d, want 12", inIssues)
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	want := issueops.ComputeAdaptiveLength(12, issueops.AdaptiveIDConfig{MaxCollisionProbability: 0.001, MinLength: 3, MaxLength: 8})
	got, err := issueops.GetAdaptiveIDLengthTx(ctx, db, "wisps", "kz-wisp")
	if err != nil {
		t.Fatalf("GetAdaptiveIDLengthTx: %v", err)
	}
	if got != want || want != 4 {
		t.Fatalf("GetAdaptiveIDLengthTx(wisps, kz-wisp) = %d with 12 promoted wisps in issues, want %d (=4)", got, want)
	}
}
