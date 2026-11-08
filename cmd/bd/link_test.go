package main

import (
	"strconv"
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

// TestLinkTypeFlagHelp pins bd link's --type usage: derived from the shared
// accepted-type list (createDepsAcceptedTypeList — the well-known types and
// both aliases), plus the custom-type allowance with its length limit and
// the non-gating caveat. The old hardcoded list named five types and neither
// alias, and could drift from what the validators accept (bd-3073gm).
func TestLinkTypeFlagHelp(t *testing.T) {
	flag := linkCmd.Flags().Lookup("type")
	if flag == nil {
		t.Fatal("bd link has no --type flag")
	}
	usage := flag.Usage

	for _, depType := range strings.Split(createDepsAcceptedTypeList(), ", ") {
		if !strings.Contains(usage, depType) {
			t.Errorf("bd link --type usage is missing %q (from createDepsAcceptedTypeList): %s", depType, usage)
		}
	}
	for _, want := range []string{
		"blocked-by",
		"depends-on",
		"Custom types",
		strconv.Itoa(types.MaxDependencyTypeLen),
		"do not gate ready work",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("bd link --type usage is missing %q: %s", want, usage)
		}
	}
}
