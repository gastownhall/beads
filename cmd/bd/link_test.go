package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestLinkDependencyType pins the resolver both bd link routes share
// (resolveLinkDependencyType): the documented aliases canonicalize to the
// gating blocks type, every other well-formed spelling passes through
// unchanged (link's custom-type contract), and only the structural limit
// refuses (GH#5585/#5560). bd dep add's validateDependencyType is
// deliberately NOT applied here — a custom type is stored as typed, inert
// edge, exactly as before.
func TestLinkDependencyType(t *testing.T) {
	t.Run("aliases canonicalize to blocks", func(t *testing.T) {
		for _, alias := range []string{"blocked-by", "depends-on"} {
			got, err := resolveLinkDependencyType(alias)
			if err != nil {
				t.Fatalf("resolveLinkDependencyType(%q): %v", alias, err)
			}
			if got != types.DepBlocks {
				t.Errorf("resolveLinkDependencyType(%q) = %q, want %q", alias, got, types.DepBlocks)
			}
		}
	})

	t.Run("canonical types pass through", func(t *testing.T) {
		for _, depType := range types.WellKnownDependencyTypes() {
			got, err := resolveLinkDependencyType(string(depType))
			if err != nil {
				t.Fatalf("resolveLinkDependencyType(%q): %v", depType, err)
			}
			if got != depType {
				t.Errorf("resolveLinkDependencyType(%q) = %q, want it unchanged", depType, got)
			}
		}
	})

	t.Run("custom types pass through unchanged", func(t *testing.T) {
		for _, custom := range []string{"owned-by", "ships-in", "excluded-from"} {
			got, err := resolveLinkDependencyType(custom)
			if err != nil {
				t.Fatalf("resolveLinkDependencyType(%q): %v", custom, err)
			}
			if got != types.DependencyType(custom) {
				t.Errorf("resolveLinkDependencyType(%q) = %q, want the spelling stored as-is", custom, got)
			}
		}
	})

	t.Run("boundary lengths", func(t *testing.T) {
		if got, err := resolveLinkDependencyType(strings.Repeat("x", types.MaxDependencyTypeLen)); err != nil {
			t.Errorf("a %d-char type is the column limit and must pass, got %v", types.MaxDependencyTypeLen, err)
		} else if got != types.DependencyType(strings.Repeat("x", types.MaxDependencyTypeLen)) {
			t.Errorf("the max-length type was not preserved: %q", got)
		}
		if _, err := resolveLinkDependencyType(strings.Repeat("x", types.MaxDependencyTypeLen+1)); err == nil {
			t.Errorf("a %d-char type must be refused", types.MaxDependencyTypeLen+1)
		}
		if _, err := resolveLinkDependencyType(""); err == nil {
			t.Error("an empty type must be refused")
		}
	})

	t.Run("invalid type keeps the raw spelling in the error", func(t *testing.T) {
		_, err := resolveLinkDependencyType("")
		if err == nil || !strings.Contains(err.Error(), "invalid dependency type") {
			t.Errorf("resolveLinkDependencyType(\"\") err = %v, want the invalid-type refusal", err)
		}
	})
}
