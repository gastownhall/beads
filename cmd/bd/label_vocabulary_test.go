//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// setupLabelVocabularyTestStore wires a fresh *dolt.DoltStore into the
// package-level store/rootCtx globals checkLabelVocabulary reads, restoring
// both on cleanup -- the same save/restore shape TestIssueIDCompletion uses.
func setupLabelVocabularyTestStore(t *testing.T) (*dolt.DoltStore, context.Context) {
	t.Helper()
	originalStore := store
	originalRootCtx := rootCtx
	t.Cleanup(func() {
		store = originalStore
		rootCtx = originalRootCtx
	})

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStoreWithPrefix(t, testDB, "lv")
	store = s

	ctx := context.Background()
	rootCtx = ctx
	return s, ctx
}

// TestCheckLabelVocabulary_OpenModeNoop pins the default: an undefined label
// is accepted silently, no matter what the registry holds, when
// labels.vocabulary is unset (the documented default is "open").
func TestCheckLabelVocabulary_OpenModeNoop(t *testing.T) {
	_, ctx := setupLabelVocabularyTestStore(t)

	out := captureStderr(t, func() {
		if err := checkLabelVocabulary(ctx, []string{"totally-undefined"}); err != nil {
			t.Fatalf("open mode must never refuse a write, got: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("open mode must never warn, got stderr: %q", out)
	}
}

// TestCheckLabelVocabulary_WarnMode pins: the write proceeds (nil error), a
// warning names the undefined label on stderr, and a case-insensitive match
// against a defined label is suggested.
func TestCheckLabelVocabulary_WarnMode(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)

	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyWarn); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.DefineLabel(ctx, "backend", "", "tester"); err != nil {
		t.Fatalf("DefineLabel: %v", err)
	}

	out := captureStderr(t, func() {
		if err := checkLabelVocabulary(ctx, []string{"Backend", "frontend"}); err != nil {
			t.Fatalf("warn mode must never refuse a write, got: %v", err)
		}
	})
	if !strings.Contains(out, `"Backend"`) {
		t.Errorf("expected the undefined case-variant named, got: %q", out)
	}
	if !strings.Contains(out, `did you mean "backend"`) {
		t.Errorf("expected a suggestion of the defined spelling, got: %q", out)
	}
	if !strings.Contains(out, `"frontend"`) {
		t.Errorf("expected the never-defined label named, got: %q", out)
	}
	if strings.Contains(out, `did you mean "frontend"`) {
		t.Errorf("must not suggest a spelling for a label with no case-variant match, got: %q", out)
	}
}

// TestCheckLabelVocabulary_WarnMode_DefinedLabelIsSilent confirms a label
// matching the registry EXACTLY produces no warning at all.
func TestCheckLabelVocabulary_WarnMode_DefinedLabelIsSilent(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)

	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyWarn); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.DefineLabel(ctx, "backend", "", "tester"); err != nil {
		t.Fatalf("DefineLabel: %v", err)
	}

	out := captureStderr(t, func() {
		if err := checkLabelVocabulary(ctx, []string{"backend"}); err != nil {
			t.Fatalf("a defined label must never be refused, got: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("a defined label must never warn, got: %q", out)
	}
}

// TestCheckLabelVocabulary_EnforceMode pins: the write is refused, and the
// error names the undefined label and the remedy.
func TestCheckLabelVocabulary_EnforceMode(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)

	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	err := checkLabelVocabulary(ctx, []string{"undefined-label"})
	if err == nil {
		t.Fatal("enforce mode must refuse an undefined label, got nil")
	}
	if !strings.Contains(err.Error(), "undefined-label") {
		t.Errorf("expected the error to name the label, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bd label define") {
		t.Errorf("expected the error to name the remedy, got: %v", err)
	}
}

// TestCheckLabelVocabulary_EnforceMode_DefinedLabelPasses confirms enforce
// mode accepts a label already in the registry.
func TestCheckLabelVocabulary_EnforceMode_DefinedLabelPasses(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)

	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.DefineLabel(ctx, "backend", "", "tester"); err != nil {
		t.Fatalf("DefineLabel: %v", err)
	}

	if err := checkLabelVocabulary(ctx, []string{"backend"}); err != nil {
		t.Fatalf("enforce mode must accept a defined label, got: %v", err)
	}
}

// TestImportBypassesLabelVocabularyEnforcement is the mandated proof (design
// doc Unit B item 4): importing an issue carrying an undefined label
// succeeds untouched even with labels.vocabulary=enforce, because the
// import/JSONL path never calls checkLabelVocabulary at all.
func TestImportBypassesLabelVocabularyEnforcement(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)

	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Deliberately NOT defining "undefined-import-label" -- enforce mode, on
	// any interactive write path, would refuse it.

	tmpDir := t.TempDir()
	jsonlContent := `{"id":"lv-import1","title":"Imported issue","type":"bug","status":"open","priority":2,"labels":["undefined-import-label"],"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
		t.Fatalf("write JSONL: %v", err)
	}

	count, err := importFromLocalJSONL(ctx, s, jsonlPath)
	if err != nil {
		t.Fatalf("import must succeed regardless of labels.vocabulary: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 issue imported, got %d", count)
	}

	labels, err := s.GetLabels(ctx, "lv-import1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	found := false
	for _, l := range labels {
		if l == "undefined-import-label" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the undefined label to survive import untouched, got: %v", labels)
	}
}

// newUpdateLabelFlagsCommand mirrors TestGatherUpdateInputNormalizesLabels'
// inline cobra.Command: gatherUpdateInput reads flags through
// Flags().Changed, which reports false for anything unregistered, so the two
// label flags exercised here are all these cases need.
func newUpdateLabelFlagsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	cmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	cmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse update flags: %v", err)
	}
	return cmd
}

// TestGatherUpdateInputEnforcesLabelVocabulary_AddLabel is the regression
// test for the proxied-server hole this call site closes: gatherUpdateInput
// is runUpdateProxiedServer's ONLY input path (the direct route in
// update.go's RunE checks --add-label/--set-labels itself, inline, and never
// calls gatherUpdateInput), so before this call site existed, `bd update
// --add-label` on a proxied deployment silently bypassed
// labels.vocabulary=enforce. Without the fix this test fails with a nil
// error.
func TestGatherUpdateInputEnforcesLabelVocabulary_AddLabel(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	cmd := newUpdateLabelFlagsCommand(t, "--add-label", "undefined-label")
	// gatherUpdateInput's refusal now goes through HandleErrorRespectJSON
	// (the --json parity fix), which PRINTS the message and returns the
	// opaque *exitError sentinel rather than the message-bearing error --
	// the same contract every other checkLabelVocabulary call site already
	// has. The label name is asserted on the captured print, not err.Error().
	var err error
	stderr := captureStderr(t, func() {
		_, err = gatherUpdateInput(ctx, cmd)
	})
	if err == nil {
		t.Fatal("enforce mode must refuse an undefined --add-label via gatherUpdateInput, got nil")
	}
	if !strings.Contains(stderr, "undefined-label") {
		t.Errorf("expected stderr to name the label, got: %q", stderr)
	}
}

// TestGatherUpdateInputEnforcesLabelVocabulary_SetLabels is the --set-labels
// twin of the above: the same gatherUpdateInput call site normalizes both
// flags, and the vocabulary check must cover both.
func TestGatherUpdateInputEnforcesLabelVocabulary_SetLabels(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	cmd := newUpdateLabelFlagsCommand(t, "--set-labels", "undefined-label")
	// See TestGatherUpdateInputEnforcesLabelVocabulary_AddLabel: the refusal
	// message is printed via HandleErrorRespectJSON, not carried in err.Error().
	var err error
	stderr := captureStderr(t, func() {
		_, err = gatherUpdateInput(ctx, cmd)
	})
	if err == nil {
		t.Fatal("enforce mode must refuse an undefined --set-labels via gatherUpdateInput, got nil")
	}
	if !strings.Contains(stderr, "undefined-label") {
		t.Errorf("expected stderr to name the label, got: %q", stderr)
	}
}

// TestGatherUpdateInputEnforcesLabelVocabulary_DefinedLabelPasses is the
// positive control: enforce mode must not refuse a label already in the
// registry, on either flag, through gatherUpdateInput.
func TestGatherUpdateInputEnforcesLabelVocabulary_DefinedLabelPasses(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.DefineLabel(ctx, "backend", "", "tester"); err != nil {
		t.Fatalf("DefineLabel: %v", err)
	}

	cmd := newUpdateLabelFlagsCommand(t, "--add-label", "backend", "--set-labels", "backend")
	in, err := gatherUpdateInput(ctx, cmd)
	if err != nil {
		t.Fatalf("enforce mode must accept a defined label, got: %v", err)
	}
	assertLabels(t, in.addLabels, []string{"backend"})
	if in.setLabels == nil {
		t.Fatal("expected --set-labels to be captured")
	}
	assertLabels(t, *in.setLabels, []string{"backend"})
}

// TestGatherUpdateInputDoesNotEnforceOnRemoveLabel pins that --remove-label
// is deliberately NOT gated: the direct route in update.go never checks it
// either (removing a label should always be allowed, undefined or not), and
// gatherUpdateInput must not diverge from that.
func TestGatherUpdateInputDoesNotEnforceOnRemoveLabel(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	if err := cmd.ParseFlags([]string{"--remove-label", "undefined-label"}); err != nil {
		t.Fatalf("parse update flags: %v", err)
	}

	in, err := gatherUpdateInput(ctx, cmd)
	if err != nil {
		t.Fatalf("--remove-label must never be refused by the vocabulary check, got: %v", err)
	}
	assertLabels(t, in.removeLabels, []string{"undefined-label"})
}

// TestGatherUpdateInputAcceptsRemovalWinsOverlap pins the contract shared
// with the in-transaction guard (GuardedLabelPatchCandidates): removal wins,
// so a label named in both --add-label and --remove-label never lands and
// must not be judged. A per-flag check refused this patch while the guarded
// layer accepted it - the CLI edge and the transaction disagreed on the same
// write.
func TestGatherUpdateInputAcceptsRemovalWinsOverlap(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	cmd := newUpdateLabelFlagsCommand(t, "--add-label", "undefined-label", "--remove-label", "undefined-label")
	in, err := gatherUpdateInput(ctx, cmd)
	if err != nil {
		t.Fatalf("removal-wins overlap must pass enforce mode (the label never lands), got: %v", err)
	}
	if len(in.addLabels) != 1 || len(in.removeLabels) != 1 {
		t.Fatalf("both flags must still reach the patch: add=%v remove=%v", in.addLabels, in.removeLabels)
	}
}

// TestGatherUpdateInputRefusesUndefinedSetMinusRemove is the discriminating
// sibling: --set-labels with an undefined label NOT covered by a removal must
// still be refused, so the overlap acceptance above cannot come from the
// check being skipped wholesale.
func TestGatherUpdateInputRefusesUndefinedSetMinusRemove(t *testing.T) {
	s, ctx := setupLabelVocabularyTestStore(t)
	if err := s.SetConfig(ctx, labelsVocabularyConfigKey, labelsVocabularyEnforce); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	cmd := newUpdateLabelFlagsCommand(t, "--set-labels", "undefined-label", "--remove-label", "other-label")
	// See TestGatherUpdateInputEnforcesLabelVocabulary_AddLabel: the refusal
	// message is printed via HandleErrorRespectJSON, not carried in err.Error().
	var err error
	stderr := captureStderr(t, func() {
		_, err = gatherUpdateInput(ctx, cmd)
	})
	if err == nil {
		t.Fatal("an undefined label in --set-labels not removed by --remove-label must still be refused")
	}
	if !strings.Contains(stderr, "undefined-label") {
		t.Errorf("expected stderr to name the label, got: %q", stderr)
	}
}
