//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// adaptiveTestCollisionProb is the max_collision_prob these tests configure. At
// the default 0.25 a hash length only moves from 3 to 4 at about 164 rows, which
// would need that many creates and promotions per test; at 0.001 it moves at 10,
// so a dozen rows straddle the same boundary and exercise the same code path.
const adaptiveTestCollisionProb = 0.001

// adaptiveIDFixtureSize is the number of rows the tests seed: on the longer side
// of the boundary adaptiveTestCollisionProb sets, whichever plane holds them.
const adaptiveIDFixtureSize = 12

// newAdaptiveTestEnv returns a test env whose adaptive hash length moves from 3
// to 4 between 9 and 10 rows.
func newAdaptiveTestEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	te := newTestEnv(t, prefix)
	err := te.store.SetConfig(t.Context(), "max_collision_prob", strconv.FormatFloat(adaptiveTestCollisionProb, 'g', -1, 64))
	if err != nil {
		t.Fatalf("set max_collision_prob: %v", err)
	}
	return te
}

// requireLengthBoundaryStraddled fails the test if small and large no longer map
// to hash lengths 3 and 4. The tests below only discriminate while the fixture
// straddles a length boundary, so a change to the adaptive formula must fail
// here loudly instead of turning them into vacuous passes.
func requireLengthBoundaryStraddled(t *testing.T, small, large int) {
	t.Helper()
	cfg := issueops.DefaultAdaptiveConfig()
	cfg.MaxCollisionProbability = adaptiveTestCollisionProb
	if got := issueops.ComputeAdaptiveLength(small, cfg); got != 3 {
		t.Fatalf("fixture no longer straddles a length boundary: ComputeAdaptiveLength(%d) = %d, want 3", small, got)
	}
	if got := issueops.ComputeAdaptiveLength(large, cfg); got != 4 {
		t.Fatalf("fixture no longer straddles a length boundary: ComputeAdaptiveLength(%d) = %d, want 4", large, got)
	}
}

// adaptiveLength reports issueops.GetAdaptiveIDLengthTx through a short-lived raw
// connection, the way the other helpers in this package reach the database. It
// also checks the connection sees the configured collision probability, so a
// config that never took effect cannot make a test pass vacuously.
func (te *testEnv) adaptiveLength(t *testing.T, ctx context.Context, table, prefix string) int {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if cfg := issueops.GetAdaptiveConfigTx(ctx, db); cfg.MaxCollisionProbability != adaptiveTestCollisionProb {
		t.Fatalf("max_collision_prob = %v, want %v", cfg.MaxCollisionProbability, adaptiveTestCollisionProb)
	}
	got, err := issueops.GetAdaptiveIDLengthTx(ctx, db, table, prefix)
	if err != nil {
		t.Fatalf("GetAdaptiveIDLengthTx(%s, %q): %v", table, prefix, err)
	}
	return got
}

// newAdaptiveIssues returns n distinct issues with no ID, so each one is
// auto-minted. Titles differ because the hash ID is a function of the title:
// identical inputs would exhaust the nonce range after a few dozen mints.
func newAdaptiveIssues(n int, label string, ephemeral bool) []*types.Issue {
	out := make([]*types.Issue, n)
	for i := range out {
		out[i] = &types.Issue{
			Title:     fmt.Sprintf("%s %d", label, i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: ephemeral,
		}
	}
	return out
}

// TestAdaptiveIDLengthCountsPromotedWisps covers the report on
// gastownhall/beads#6754 (comment 5860466950): a promoted wisp moves into issues
// under its unchanged <prefix>-wisp-* ID, so a wisp mint that counts only the
// wisps table never sees the -wisp- namespace fill up and keeps handing out the
// shortest hash. Live wisps are reaped while promoted ones accumulate, which is
// what makes the count diverge from the namespace's real size.
func TestAdaptiveIDLengthCountsPromotedWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newAdaptiveTestEnv(t, "kz")
	requireLengthBoundaryStraddled(t, 0, adaptiveIDFixtureSize)

	wisps := newAdaptiveIssues(adaptiveIDFixtureSize, "promoted wisp", true)
	if err := te.store.CreateIssues(ctx, wisps, "tester"); err != nil {
		t.Fatalf("create wisps: %v", err)
	}
	for _, w := range wisps {
		if err := te.store.PromoteFromEphemeral(ctx, w.ID, "tester"); err != nil {
			t.Fatalf("promote %s: %v", w.ID, err)
		}
	}

	// Precondition: promotion emptied the wisps table and moved every row into
	// issues, so the wisps-table count alone reads zero.
	var inIssues, inWisps int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'kz-wisp-%'", nil, &inIssues)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps", nil, &inWisps)
	if inIssues != adaptiveIDFixtureSize || inWisps != 0 {
		t.Fatalf("after promotion: issues kz-wisp-* rows = %d, wisps rows = %d; want %d and 0", inIssues, inWisps, adaptiveIDFixtureSize)
	}

	if got := te.adaptiveLength(t, ctx, "wisps", "kz-wisp"); got != 4 {
		t.Errorf("GetAdaptiveIDLengthTx(wisps, kz-wisp) = %d with %d promoted wisps in issues, want 4", got, adaptiveIDFixtureSize)
	}

	// End to end: the next wisp the store auto-mints takes the longer hash.
	next := &types.Issue{
		Title:     "next wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		CreatedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
	}
	if err := te.store.CreateIssue(ctx, next, "tester"); err != nil {
		t.Fatalf("create next wisp: %v", err)
	}
	if !strings.HasPrefix(next.ID, "kz-wisp-") {
		t.Fatalf("next wisp ID = %q, want a kz-wisp-* ID", next.ID)
	}
	if hash := strings.TrimPrefix(next.ID, "kz-wisp-"); len(hash) != 4 {
		t.Errorf("next wisp ID = %q: hash %q has %d chars, want 4", next.ID, hash, len(hash))
	}
}

// TestAdaptiveIDLengthLiveWispsDoNotLengthenBasePrefix guards the other side of
// the same change. A live wisp's ID is <prefix>-wisp-<hash>, which the LIKE
// '<prefix>-%' pattern matches, but no <prefix>-<hash> ID can ever equal one
// (hash IDs are pure base36, see idgen.GenerateHashID). Summing the plain
// prefix count over both planes would therefore lengthen every base-prefix ID
// because of unrelated wisps; only the prefix's exact namespace may be added
// from the other plane.
func TestAdaptiveIDLengthLiveWispsDoNotLengthenBasePrefix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newAdaptiveTestEnv(t, "nv")
	const durable = 3
	requireLengthBoundaryStraddled(t, durable, durable+adaptiveIDFixtureSize)

	if err := te.store.CreateIssues(ctx, newAdaptiveIssues(durable, "durable issue", false), "tester"); err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := te.store.CreateIssues(ctx, newAdaptiveIssues(adaptiveIDFixtureSize, "live wisp", true), "tester"); err != nil {
		t.Fatalf("create wisps: %v", err)
	}

	// Precondition, and the reason this test discriminates: the wisps all match
	// the base prefix's LIKE pattern, so a sum of the plain prefix count over
	// both planes reads durable+adaptiveIDFixtureSize and picks length 4.
	var issueRows, wispRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'nv-%'", nil, &issueRows)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id LIKE 'nv-%'", nil, &wispRows)
	if issueRows != durable || wispRows != adaptiveIDFixtureSize {
		t.Fatalf("fixture rows matching 'nv-%%': issues = %d, wisps = %d; want %d and %d", issueRows, wispRows, durable, adaptiveIDFixtureSize)
	}

	if got := te.adaptiveLength(t, ctx, "issues", "nv"); got != 3 {
		t.Errorf("GetAdaptiveIDLengthTx(issues, nv) = %d with %d issues and %d live nv-wisp-* wisps, want 3", got, durable, adaptiveIDFixtureSize)
	}
}

// TestAdaptiveIDLengthIssueMintCountsBaseNamespaceHeldInWisps is the mirror of
// the promoted-wisp case: issues and wisps share one ID space, and a demoted
// issue keeps its <prefix>-<hash> ID in the wisps table. An issue mint that
// counts only issues would not see those IDs, so the base namespace could fill
// up in wisps while its hash length stayed at the minimum.
func TestAdaptiveIDLengthIssueMintCountsBaseNamespaceHeldInWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newAdaptiveTestEnv(t, "dm")
	requireLengthBoundaryStraddled(t, 0, adaptiveIDFixtureSize)

	// Explicit dm-<3 base36 chars> IDs, all distinct: the shape a demoted issue
	// has in the wisps table. 36*36 is the first three-digit base36 number.
	wisps := make([]*types.Issue, adaptiveIDFixtureSize)
	for i := range wisps {
		wisps[i] = &types.Issue{
			ID:        "dm-" + strconv.FormatInt(int64(36*36+i), 36),
			Title:     fmt.Sprintf("demoted issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
	}
	if err := te.store.CreateIssues(ctx, wisps, "tester"); err != nil {
		t.Fatalf("create wisps: %v", err)
	}

	// Precondition: the base namespace lives entirely in wisps.
	var inIssues, inWisps int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues", nil, &inIssues)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id LIKE 'dm-%'", nil, &inWisps)
	if inIssues != 0 || inWisps != adaptiveIDFixtureSize {
		t.Fatalf("fixture rows: issues = %d, wisps dm-* = %d; want 0 and %d", inIssues, inWisps, adaptiveIDFixtureSize)
	}

	if got := te.adaptiveLength(t, ctx, "issues", "dm"); got != 4 {
		t.Errorf("GetAdaptiveIDLengthTx(issues, dm) = %d with %d dm-<hash> rows in wisps, want 4", got, adaptiveIDFixtureSize)
	}
}
