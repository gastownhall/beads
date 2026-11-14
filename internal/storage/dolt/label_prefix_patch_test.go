package dolt

import (
	"errors"
	"slices"
	"strings"
	"testing"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The prefix arm of the label algebra (GH#5657's dimension swap): after an
// optional whole-set Replace, every label matching a RemovePrefixes entry is
// dropped, then Add lands, then Remove — removal wins. Prefixes match
// LITERALLY (% and _ are characters, not wildcards), an empty entry is
// dropped rather than wiping the set, and an over-length prefix is refused
// before any write so the whole request rolls back. Every case here drives
// the shared issueops execute body on one transaction, the same SQL the
// durable and embedded stores' write paths run.

// TestLabelPatchRemovesByPrefix runs the prefix algebra's edge cases against
// a durable issue, one issue per case so the cases share one store and branch
// without sharing rows.
func TestLabelPatchRemovesByPrefix(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	applyPatch := func(t *testing.T, id string, patch publicops.LabelPatch) (publicops.UpdateResult, error) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin label patch transaction: %v", err)
		}
		defer tx.Rollback()
		result, _, err := storageissueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
			Actor:   "tester",
			IssueID: id,
			Patch:   publicops.IssuePatch{Labels: patch},
		})
		if err != nil {
			return publicops.UpdateResult{}, err
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit label patch: %v", err)
		}
		return result, nil
	}
	seed := func(t *testing.T, id string, labels ...string) {
		t.Helper()
		createPerm(t, ctx, store, id)
		for _, label := range labels {
			if err := store.AddLabel(ctx, id, label, "tester"); err != nil {
				t.Fatalf("seed %s on %s: %v", label, id, err)
			}
		}
	}
	stored := func(t *testing.T, id string) []string {
		t.Helper()
		return readIssueLabels(t, ctx, store, "labels", id)
	}

	t.Run("DropsEveryLabelUnderThePrefixAndKeepsTheRest", func(t *testing.T) {
		const id = "lpp-drop"
		seed(t, id, "state:a", "state:b", "stateful", "area:x")
		// The duplicate entry pins that a repeated prefix deletes each label
		// once rather than tripping the algebra.
		result, err := applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{"state:", "state:"}})
		if err != nil {
			t.Fatalf("prefix removal: %v", err)
		}
		if !result.Changed {
			t.Fatal("a patch that dropped two labels must report a change")
		}
		want := []string{"area:x", "stateful"}
		if got := stored(t, id); !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v", got, want)
		}
		if !slices.Equal(result.Issue.Labels, want) {
			t.Fatalf("hydrated result labels = %v, want %v", result.Issue.Labels, want)
		}
	})

	t.Run("PercentAndUnderscoreAreCharactersNotWildcards", func(t *testing.T) {
		const id = "lpp-literal"
		seed(t, id, "%auto", "auto", "sl_draft", "xdraft")
		if _, err := applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{"%a", "sl_"}}); err != nil {
			t.Fatalf("literal prefix removal: %v", err)
		}
		// "%a" must take %auto and leave auto; "sl_" must take sl_draft and
		// leave xdraft. A wildcard read of either prefix would take the
		// wrong half.
		if got, want := stored(t, id), []string{"auto", "xdraft"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v", got, want)
		}
	})

	t.Run("AnOverlengthPrefixRollsBackTheWholeRequest", func(t *testing.T) {
		const id = "lpp-toolong"
		seed(t, id, "state:a")
		_, err := applyPatch(t, id, publicops.LabelPatch{
			RemovePrefixes: []string{strings.Repeat("a", types.MaxFieldLen+1)},
			Add:            []string{"area:y"},
		})
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("over-length prefix error = %v, want ErrFieldTooLong", err)
		}
		// The prefix is refused BEFORE any write, so the valid Add riding the
		// same request must not land either.
		if got, want := stored(t, id), []string{"state:a"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels after refusal = %v, want the seeded %v: the refusal leaked a partial write", got, want)
		}
	})

	t.Run("AnEmptyPrefixIsDroppedNotAWipe", func(t *testing.T) {
		const id = "lpp-empty"
		seed(t, id, "state:a", "keep")
		result, err := applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{"", "state:"}})
		if err != nil {
			t.Fatalf("prefix removal with an empty entry: %v", err)
		}
		if !result.Changed {
			t.Fatal("dropping state:a must report a change")
		}
		if got, want := stored(t, id), []string{"keep"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v: the empty prefix wiped the set", got, want)
		}
		// An empty prefix ALONE matches nothing and refuses nothing: the
		// patch is a semantic no-op.
		result, err = applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{""}})
		if err != nil {
			t.Fatalf("empty-only prefix patch: %v", err)
		}
		if result.Changed {
			t.Fatal("an empty-only prefix patch must report no change")
		}
		if got, want := stored(t, id), []string{"keep"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v", got, want)
		}
	})

	t.Run("OverlappingPrefixesDeleteEachLabelOnce", func(t *testing.T) {
		const id = "lpp-overlap"
		seed(t, id, "state:a", "size:lg")
		if _, err := applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{"s", "state:"}}); err != nil {
			t.Fatalf("overlapping prefix removal: %v", err)
		}
		// "s" already takes both rows; "state:" must find nothing left to
		// trip over (deletion-during-range is the hazard this pins).
		if got := stored(t, id); len(got) != 0 {
			t.Fatalf("stored labels = %v, want the empty set", got)
		}
	})

	t.Run("ReplaceThenPrefixesThenAddThenRemoveInOrder", func(t *testing.T) {
		const id = "lpp-order"
		seed(t, id, "state:a")
		if _, err := applyPatch(t, id, publicops.LabelPatch{
			Replace:        publicops.Field[[]string]{Set: true, Value: []string{"state:a", "state:b", "keep"}},
			RemovePrefixes: []string{"state:"},
			Add:            []string{"state:c"},
			Remove:         []string{"keep"},
		}); err != nil {
			t.Fatalf("four-step patch: %v", err)
		}
		// Replace seeds state:a/b/keep, the prefix drops both state labels,
		// the Add re-lands state:c (which is why it runs AFTER the prefix),
		// and the explicit Remove takes keep. Any other order spells a
		// different set.
		if got, want := stored(t, id), []string{"state:c"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v", got, want)
		}
	})

	t.Run("AnExplicitRemoveWinsOverTheReLandedAdd", func(t *testing.T) {
		const id = "lpp-remove-wins"
		seed(t, id, "state:a")
		if _, err := applyPatch(t, id, publicops.LabelPatch{
			RemovePrefixes: []string{"state:"},
			Add:            []string{"state:c"},
			Remove:         []string{"state:c"},
		}); err != nil {
			t.Fatalf("prefix, add and remove of one name: %v", err)
		}
		if got := stored(t, id); len(got) != 0 {
			t.Fatalf("stored labels = %v, want the empty set: the remove did not win", got)
		}
	})

	t.Run("APrefixMatchingNothingIsANoOp", func(t *testing.T) {
		const id = "lpp-nomatch"
		seed(t, id, "state:a")
		result, err := applyPatch(t, id, publicops.LabelPatch{RemovePrefixes: []string{"zzz:"}})
		if err != nil {
			t.Fatalf("no-match prefix patch: %v", err)
		}
		if result.Changed {
			t.Fatal("a prefix matching nothing must report no change")
		}
		if got, want := stored(t, id), []string{"state:a"}; !slices.Equal(got, want) {
			t.Fatalf("stored labels = %v, want %v", got, want)
		}
	})

	t.Run("TheSameAlgebraRunsOnTheWispTier", func(t *testing.T) {
		const id = "lpp-wisp"
		createWisp(t, ctx, store, id)
		for _, label := range []string{"state:a", "state:b", "keep"} {
			if err := store.AddLabel(ctx, id, label, "tester"); err != nil {
				t.Fatalf("seed %s: %v", label, err)
			}
		}
		if _, err := applyPatch(t, id, publicops.LabelPatch{
			RemovePrefixes: []string{"state:"},
			Add:            []string{"state:c"},
		}); err != nil {
			t.Fatalf("wisp prefix patch: %v", err)
		}
		if got, want := readIssueLabels(t, ctx, store, "wisp_labels", id), []string{"keep", "state:c"}; !slices.Equal(got, want) {
			t.Fatalf("stored wisp labels = %v, want %v", got, want)
		}
		if got := readIssueLabels(t, ctx, store, "labels", id); len(got) != 0 {
			t.Fatalf("wisp patch landed durable rows too: %v", got)
		}
	})
}
