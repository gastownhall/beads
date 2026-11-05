//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestMintedIDsPassOverEveryHeldID pins that a create which names no id is
// never refused as ErrAlreadyExists for the id it was handed, through the body
// both store backends share (issueops.ExecuteCreateBatch and ExecuteCreate).
//
// Each case FORCES its collision rather than waiting for a three-character
// draw to repeat. The hash mint is a pure function of the prefix, title,
// description, actor, created_at, length and nonce, so the test computes the
// ids a create will try first and occupies them, or sends the same inputs
// twice, before it asks. The two CI flakes this pins were the same draws at
// random: TestBatchCreatorContract's "issue already exists: bcr-62m" (two items
// of one batch handed one id) and TestE2E_ExportOverHTTP's "issue already
// exists: e2e-wisp-czk" (a wisp handed the id of an imported row in issues).
func TestMintedIDsPassOverEveryHeldID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mc")
	ctx := t.Context()
	creator, err := te.store.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const actor = "minter"
	at := time.Date(2022, 1, 2, 3, 4, 5, 678901234, time.UTC)
	issue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Priority: 2, IssueType: types.TypeTask, CreatedAt: at}
	}
	// firstCandidates are the nonce-0 ids a mint under prefix tries first, at
	// every length it can start at: the adaptive length follows the plane's
	// row count, which the cases before change.
	firstCandidates := func(prefix, title string) []string {
		var ids []string
		for length := 3; length <= 8; length++ {
			ids = append(ids, idgen.GenerateHashID(prefix, title, "", actor, at, length, 0))
		}
		return ids
	}
	occupy := func(t *testing.T, ids []string, ephemeral bool) {
		t.Helper()
		for _, id := range ids {
			occupant := &types.Issue{ID: id, Title: "occupant", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
			if err := te.store.CreateIssue(ctx, occupant, "seeder"); err != nil {
				t.Fatalf("occupy %s: %v", id, err)
			}
		}
	}
	createOne := func(t *testing.T, item *types.Issue) string {
		t.Helper()
		result, err := creator.CreateBatch(ctx, publicops.CreateBatchRequest{
			Actor: actor,
			Items: []publicops.BatchCreateItem{{Issue: item}},
		})
		if err != nil {
			t.Fatalf("CreateBatch(%q) = %v, want the mint to pass over every held id", item.Title, err)
		}
		return result.Issues[0].ID
	}

	t.Run("two items of one batch", func(t *testing.T) {
		// ExecuteCreateBatch assigns every id before it writes any row, so
		// the second item's probe cannot see the first item's id.
		result, err := creator.CreateBatch(ctx, publicops.CreateBatchRequest{
			Actor: actor,
			Items: []publicops.BatchCreateItem{{Issue: issue("twin")}, {Issue: issue("twin")}},
		})
		if err != nil {
			t.Fatalf("CreateBatch(two identical items) = %v", err)
		}
		first, second := result.Issues[0].ID, result.Issues[1].ID
		if first == second {
			t.Fatalf("both items were handed %s", first)
		}
		te.assertRowExists(t, ctx, "issues", first)
		te.assertRowExists(t, ctx, "issues", second)
	})

	t.Run("a durable mint over a wisp", func(t *testing.T) {
		held := firstCandidates("mc", "durable over wisp")
		occupy(t, held, true)
		id := createOne(t, issue("durable over wisp"))
		if slices.Contains(held, id) {
			t.Fatalf("minted %s, which a wisp holds", id)
		}
		te.assertRowExists(t, ctx, "issues", id)
		te.assertRowNotExists(t, ctx, "wisps", id)
	})

	t.Run("a wisp mint over a durable issue", func(t *testing.T) {
		// The E2E shape: an imported (or promoted) wisp's id sits in issues
		// under the -wisp- prefix, and a local no-history create mints there.
		held := firstCandidates("mc-wisp", "wisp over durable")
		occupy(t, held, false)
		item := issue("wisp over durable")
		item.NoHistory = true
		created, err := lifecycle.Create(ctx, publicops.CreateRequest{Actor: actor, Issue: item})
		if err != nil {
			t.Fatalf("Create(no-history) = %v, want the mint to pass over the ids issues holds", err)
		}
		id := created.Issue.ID
		if slices.Contains(held, id) {
			t.Fatalf("minted %s, which a durable issue holds", id)
		}
		te.assertRowExists(t, ctx, "wisps", id)
		te.assertRowNotExists(t, ctx, "issues", id)
	})

	t.Run("a mint over a row of its own plane", func(t *testing.T) {
		held := firstCandidates("mc", "durable over durable")
		occupy(t, held, false)
		id := createOne(t, issue("durable over durable"))
		if slices.Contains(held, id) {
			t.Fatalf("minted %s, which a durable issue holds", id)
		}
		te.assertRowExists(t, ctx, "issues", id)
	})

	t.Run("a mint before explicit items naming its candidates", func(t *testing.T) {
		// The later items are in no table when the first item is minted, so
		// only the batch's own claimed set can steer the mint off their ids.
		items := []publicops.BatchCreateItem{{Issue: issue("minted first")}}
		named := firstCandidates("mc", "minted first")
		for _, id := range named {
			explicit := issue("named later")
			explicit.ID = id
			items = append(items, publicops.BatchCreateItem{Issue: explicit})
		}
		result, err := creator.CreateBatch(ctx, publicops.CreateBatchRequest{Actor: actor, Items: items})
		if err != nil {
			t.Fatalf("CreateBatch(minted, then explicit ids naming its candidates) = %v", err)
		}
		if id := result.Issues[0].ID; slices.Contains(named, id) {
			t.Fatalf("minted %s, which a later item of the batch names", id)
		}
		for i, id := range named {
			if got := result.Issues[i+1].ID; got != id {
				t.Errorf("explicit item %d landed as %s, want %s", i+1, got, id)
			}
		}
	})

	t.Run("a counter mint before an explicit item naming its value", func(t *testing.T) {
		// The counter half of the claimed set. A fresh workspace's counter
		// starts at 1, so [minted, explicit cm-1] would hand the minted item
		// cm-1 and then refuse the explicit one as occupied by its own batch.
		te := newTestEnv(t, "cm")
		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig(issue_id_mode=counter): %v", err)
		}
		creator, err := te.store.BatchCreator()
		if err != nil {
			t.Fatalf("BatchCreator(): %v", err)
		}
		explicit := issue("explicit")
		explicit.ID = "cm-1"
		result, err := creator.CreateBatch(ctx, publicops.CreateBatchRequest{
			Actor: actor,
			Items: []publicops.BatchCreateItem{{Issue: issue("minted")}, {Issue: explicit}},
		})
		if err != nil {
			t.Fatalf("CreateBatch([minted, explicit cm-1]) = %v", err)
		}
		if got := result.Issues[0].ID; got != "cm-2" {
			t.Errorf("minted item = %s, want cm-2, the counter value after the claimed cm-1", got)
		}
		if got := result.Issues[1].ID; got != "cm-1" {
			t.Errorf("explicit item = %s, want cm-1", got)
		}
	})
}
