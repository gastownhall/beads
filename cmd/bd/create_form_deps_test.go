package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestParseFormDeps pins the create form's dependency parsing: the same
// per-entry parser as `bd create --deps` (parseDepSpec), with the form's one
// deliberate divergence pinned before anything else — direction. A form entry
// has always meant "the new issue depends on the target", including
// `blocks:<id>`, the opposite of --deps' explicit blocks: swap (bd-3073gm,
// GH#5585/#5560).
func TestParseFormDeps(t *testing.T) {
	t.Run("bare id is a blocks edge", func(t *testing.T) {
		got, err := parseFormDeps([]string{"bd-1"})
		if err != nil {
			t.Fatalf("parseFormDeps: %v", err)
		}
		want := []domain.DependencySpec{{Type: types.DepBlocks, TargetID: "bd-1"}}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("parseFormDeps(bd-1) = %+v, want %+v", got, want)
		}
	})

	t.Run("every depends-on spelling keeps the historical direction", func(t *testing.T) {
		for _, entry := range []string{"bd-2", "blocks:bd-2", "blocked-by:bd-2", "depends-on:bd-2"} {
			got, err := parseFormDeps([]string{entry})
			if err != nil {
				t.Fatalf("parseFormDeps(%q): %v", entry, err)
			}
			want := domain.DependencySpec{Type: types.DepBlocks, TargetID: "bd-2"}
			if len(got) != 1 || got[0] != want {
				t.Errorf("parseFormDeps(%q) = %+v, want the single non-swapped %+v", entry, got, want)
			}
		}
	})

	t.Run("other types keep their spelling", func(t *testing.T) {
		got, err := parseFormDeps([]string{"related: bd-3", "discovered-from:bd-4"})
		if err != nil {
			t.Fatalf("parseFormDeps: %v", err)
		}
		want := []domain.DependencySpec{
			{Type: types.DepRelated, TargetID: "bd-3"},
			{Type: types.DepDiscoveredFrom, TargetID: "bd-4"},
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("parseFormDeps = %+v, want %+v", got, want)
		}
	})

	t.Run("whitespace and empty entries are skipped", func(t *testing.T) {
		got, err := parseFormDeps([]string{"", "   ", "  bd-5  "})
		if err != nil {
			t.Fatalf("parseFormDeps: %v", err)
		}
		if len(got) != 1 || got[0].TargetID != "bd-5" {
			t.Errorf("parseFormDeps = %+v, want the one trimmed bd-5 edge", got)
		}
	})

	t.Run("typed entry with an empty target is refused", func(t *testing.T) {
		for _, entry := range []string{"blocks:", "blocked-by:   "} {
			if _, err := parseFormDeps([]string{entry}); err == nil || !strings.Contains(err.Error(), "target is empty") {
				t.Errorf("parseFormDeps(%q) err = %v, want the empty-target refusal", entry, err)
			}
		}
	})

	t.Run("unknown type is refused with the accepted vocabulary", func(t *testing.T) {
		_, err := parseFormDeps([]string{"owned-by:bd-6"})
		if err == nil || !strings.Contains(err.Error(), "owned-by") || !strings.Contains(err.Error(), "valid types") {
			t.Errorf("parseFormDeps(owned-by:bd-6) err = %v, want the unknown-type refusal naming the spelling and the vocabulary", err)
		}
	})

	t.Run("canonical-equivalent repeats collapse to one edge", func(t *testing.T) {
		for name, tc := range map[string]struct {
			deps     []string
			wantType types.DependencyType
		}{
			"both aliases":              {[]string{"blocked-by:bd-1", "depends-on:bd-1"}, types.DepBlocks},
			"explicit blocks and alias": {[]string{"blocks:bd-1", "depends-on:bd-1"}, types.DepBlocks},
			"bare id and alias":         {[]string{"bd-1", "blocked-by:bd-1"}, types.DepBlocks},
			"identical repeat":          {[]string{"related:bd-1", "related:bd-1"}, types.DepRelated},
		} {
			got, err := parseFormDeps(tc.deps)
			if err != nil {
				t.Fatalf("%s: equivalent spellings must collapse, not conflict: %v", name, err)
			}
			if len(got) != 1 || got[0].Type != tc.wantType || got[0].TargetID != "bd-1" {
				t.Errorf("%s: parseFormDeps(%q) = %+v, want the single collapsed %s edge", name, tc.deps, got, tc.wantType)
			}
		}
	})
}
