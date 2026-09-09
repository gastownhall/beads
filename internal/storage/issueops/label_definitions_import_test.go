package issueops

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func strPtr(s string) *string { return &s }

// TestImportLabelDefinitions pins the define-if-absent import contract for
// vocabulary records (the JSONL transport half of the registry, bd label
// define): exact re-imports are silent no-ops, case-insensitive collisions
// keep the existing definition and warn, absent labels are defined with the
// record's own created_by (falling back to the batch actor), a define
// refused with storage.ErrValidation degrades to a warning, and any other
// storage failure fails the import.
func TestImportLabelDefinitions(t *testing.T) {
	ctx := context.Background()
	existing := []types.LabelDefinition{
		{Label: "backend", Description: strPtr("server work")},
		{Label: "tier:opus"},
	}

	t.Run("DefinesAbsentSkipsExactWarnsOnCaseCollision", func(t *testing.T) {
		var defined []string
		var actors []string
		define := func(_ context.Context, label, _, actor string) error {
			defined = append(defined, label)
			actors = append(actors, actor)
			return nil
		}
		incoming := []types.LabelDefinition{
			{Label: "backend"},                              // exact: silent skip
			{Label: "Tier:Opus"},                            // case collision: warn + keep
			{Label: "frontend", CreatedBy: strPtr("alice")}, // absent: defined as alice
			{Label: "urgent"},                               // absent: defined as fallback
			{Label: "URGENT"},                               // intra-batch case collision: warn
			{Label: ""},                                     // empty: ignored
			{Label: "frontend", Description: strPtr("dup incoming")}, // exact vs just-defined: skip
		}
		count, warnings, err := ImportLabelDefinitions(ctx, existing, incoming, "import", define)
		if err != nil {
			t.Fatalf("ImportLabelDefinitions: %v", err)
		}
		if count != 2 {
			t.Fatalf("expected 2 defined, got %d (%v)", count, defined)
		}
		if len(defined) != 2 || defined[0] != "frontend" || defined[1] != "urgent" {
			t.Fatalf("unexpected define calls: %v", defined)
		}
		if actors[0] != "alice" || actors[1] != "import" {
			t.Fatalf("unexpected actors: %v", actors)
		}
		if len(warnings) != 2 {
			t.Fatalf("expected 2 warnings (Tier:Opus, URGENT), got %v", warnings)
		}
	})

	t.Run("ValidationErrorDegradesToWarning", func(t *testing.T) {
		define := func(_ context.Context, label, _, _ string) error {
			return fmt.Errorf("%w: label %q collides", storage.ErrValidation, label)
		}
		count, warnings, err := ImportLabelDefinitions(ctx, nil,
			[]types.LabelDefinition{{Label: "racy"}}, "import", define)
		if err != nil {
			t.Fatalf("a validation refusal must not fail the import: %v", err)
		}
		if count != 0 || len(warnings) != 1 {
			t.Fatalf("expected 0 defined + 1 warning, got %d/%v", count, warnings)
		}
	})

	t.Run("StorageErrorFailsTheImport", func(t *testing.T) {
		boom := errors.New("disk on fire")
		define := func(_ context.Context, _, _, _ string) error { return boom }
		_, _, err := ImportLabelDefinitions(ctx, nil,
			[]types.LabelDefinition{{Label: "x"}}, "import", define)
		if !errors.Is(err, boom) {
			t.Fatalf("expected the storage error to propagate, got %v", err)
		}
	})
}
