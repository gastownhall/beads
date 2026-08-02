package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func TestParseDepSpecs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []domain.DependencySpec
		wantErr bool
	}{
		{
			name: "empty input",
			in:   nil,
			want: nil,
		},
		{
			name: "whitespace and empties skipped",
			in:   []string{"", "  "},
			want: nil,
		},
		{
			name: "bare id becomes blocks edge",
			in:   []string{"bd-1"},
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-1"},
			},
		},
		{
			name: "depends-on alias maps to blocks",
			in:   []string{"depends-on:bd-1"},
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-1"},
			},
		},
		{
			name: "blocked-by alias maps to blocks",
			in:   []string{"blocked-by:bd-2"},
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-2"},
			},
		},
		{
			name: "explicit blocks swaps direction",
			in:   []string{"blocks:bd-3"},
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-3", SwapDirection: true},
			},
		},
		{
			name: "discovered-from preserved as typed edge",
			in:   []string{"discovered-from:bd-4"},
			want: []domain.DependencySpec{
				{Type: types.DepDiscoveredFrom, TargetID: "bd-4"},
			},
		},
		{
			name: "parent-child typed edge",
			in:   []string{"parent-child:bd-5"},
			want: []domain.DependencySpec{
				{Type: types.DepParentChild, TargetID: "bd-5"},
			},
		},
		{
			name: "multiple entries with whitespace trimmed",
			in:   []string{"  bd-1  ", "blocks: bd-2 ", "discovered-from:bd-3"},
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-1"},
				{Type: types.DepBlocks, TargetID: "bd-2", SwapDirection: true},
				{Type: types.DepDiscoveredFrom, TargetID: "bd-3"},
			},
		},
		{
			name:    "unknown type rejected",
			in:      []string{"nonsense:bd-1"},
			wantErr: true,
		},
		{
			name:    "empty type rejected",
			in:      []string{":bd-1"},
			wantErr: true,
		},
		{
			// Cobra's StringSlice flag CSV-decodes "--deps a,b" into two
			// elements before parseDepSpecs ever sees them; this is the
			// representation parseDepSpecs actually receives in production,
			// not a single comma-joined string (parseDepSpecs must not
			// re-split on "," or it double-decodes a CSV-quoted value that
			// legitimately contains a comma).
			name: "multi-type different targets (already split by cobra)",
			in:   []string{"discovered-from:bd-20", "blocks:bd-15"},
			want: []domain.DependencySpec{
				{Type: types.DepDiscoveredFrom, TargetID: "bd-20"},
				{Type: types.DepBlocks, TargetID: "bd-15", SwapDirection: true},
			},
		},
		{
			name:    "multi-type same target rejected",
			in:      []string{"discovered-from:bd-1", "blocked-by:bd-1"},
			wantErr: true,
		},
		{
			name: "duplicate identical edge is deduped, not rejected",
			in:   []string{"blocked-by:bd-1", "depends-on:bd-1"},
			// Both aliases normalize to the same {DepBlocks, bd-1, no swap}
			// edge; storage already treats a repeated identical add as
			// idempotent, so this must dedupe rather than error.
			want: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "bd-1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDepSpecs(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDepSpecs(%v) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDepSpecs(%v) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDepSpecs(%v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestCanonicalDependencyType is a table test for the alias-normalization
// helper shared by `bd create --deps` (parseDepSpec) and `bd dep add --type`
// (GH#5069): both "blocked-by" and "depends-on" must map to the canonical
// "blocks" type that ready/blocked gating checks, and any other value
// (well-known or custom) must pass through unchanged.
func TestCanonicalDependencyType(t *testing.T) {
	tests := []struct {
		name string
		in   types.DependencyType
		want types.DependencyType
	}{
		{"blocked-by aliases to blocks", "blocked-by", types.DepBlocks},
		{"depends-on aliases to blocks", "depends-on", types.DepBlocks},
		{"blocks passes through unchanged", types.DepBlocks, types.DepBlocks},
		{"parent-child passes through unchanged", types.DepParentChild, types.DepParentChild},
		{"discovered-from passes through unchanged", types.DepDiscoveredFrom, types.DepDiscoveredFrom},
		{"unknown type passes through unchanged", "totally-custom", "totally-custom"},
		{"empty type passes through unchanged", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalDependencyType(tt.in); got != tt.want {
				t.Errorf("canonicalDependencyType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestValidateDependencyType covers the shared validity/well-known-ness gate
// used by both `bd create --deps` and `bd dep add --type`. Per the intent
// documented on types.WellKnownDependencyTypes, both commands reject
// custom/unknown dependency types identically.
func TestValidateDependencyType(t *testing.T) {
	tests := []struct {
		name    string
		in      types.DependencyType
		wantErr bool
	}{
		{"canonical blocks accepted", types.DepBlocks, false},
		{"parent-child accepted", types.DepParentChild, false},
		{"discovered-from accepted", types.DepDiscoveredFrom, false},
		{"related accepted", types.DepRelated, false},
		{"empty type rejected", "", true},
		{"unknown/custom type rejected", "totally-custom", true},
		{"unnormalized alias rejected (caller must normalize first)", "blocked-by", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDependencyType(tt.in)
			if tt.wantErr && err == nil {
				t.Fatalf("validateDependencyType(%q) = nil, want error", tt.in)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateDependencyType(%q) unexpected error: %v", tt.in, err)
			}
		})
	}
}

func TestBuildWaitsFor(t *testing.T) {
	t.Run("empty spawner without explicit gate returns nil", func(t *testing.T) {
		got, err := buildWaitsFor("", "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})
	t.Run("explicit gate without spawner returns error", func(t *testing.T) {
		// --waits-for-gate set but --waits-for absent: must be rejected,
		// not silently ignored. Applies to any gate value, including the
		// valid "all-children" — the operator clearly intended a dep they
		// did not get.
		_, err := buildWaitsFor("", types.WaitsForAllChildren, true)
		if err == nil {
			t.Fatal("expected error when --waits-for-gate is explicit but spawner is empty")
		}
		_, err = buildWaitsFor("", "TOTALLY-BOGUS", true)
		if err == nil {
			t.Fatal("expected error when --waits-for-gate is explicit (invalid value) but spawner is empty")
		}
	})
	t.Run("empty gate defaults to all-children", func(t *testing.T) {
		got, err := buildWaitsFor("bd-1", "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := &domain.WaitsForSpec{SpawnerID: "bd-1", Gate: types.WaitsForAllChildren}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("any-children gate accepted", func(t *testing.T) {
		got, err := buildWaitsFor("bd-1", types.WaitsForAnyChildren, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Gate != types.WaitsForAnyChildren {
			t.Errorf("got gate %q, want %q", got.Gate, types.WaitsForAnyChildren)
		}
	})
	t.Run("invalid gate rejected", func(t *testing.T) {
		_, err := buildWaitsFor("bd-1", "bogus", false)
		if err == nil {
			t.Fatal("expected error for invalid gate")
		}
	})
	t.Run("whitespace spawner treated as empty", func(t *testing.T) {
		got, err := buildWaitsFor("   ", "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil for whitespace spawner, got %+v", got)
		}
	})
}

func TestDiscoveredFromParent(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"none", []string{"bd-1", "blocks:bd-2"}, ""},
		{"present", []string{"bd-1", "discovered-from:bd-99", "blocks:bd-2"}, "bd-99"},
		{"first wins", []string{"discovered-from:bd-7", "discovered-from:bd-8"}, "bd-7"},
		{"empty target ignored", []string{"discovered-from:", "discovered-from:bd-9"}, "bd-9"},
		{"whitespace trimmed", []string{"  discovered-from: bd-5 "}, "bd-5"},
		{"empty input", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discoveredFromParent(tt.in)
			if got != tt.want {
				t.Errorf("discoveredFromParent(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveDepSpecTargetsNormalizesBareSlug(t *testing.T) {
	st := &createAtomicFakeStore{}
	specs := []domain.DependencySpec{
		{Type: types.DepDiscoveredFrom, TargetID: "8vezf"},
		{Type: types.DepBlocks, TargetID: "fake-already-full"},
		{Type: types.DepRelated, TargetID: "external:other-system/42"},
	}
	got, err := resolveDepSpecTargets(context.Background(), st, specs)
	if err != nil {
		t.Fatalf("resolveDepSpecTargets: %v", err)
	}
	if got[0].TargetID != "fake-8vezf" {
		t.Errorf("bare slug resolved to %q, want fake-8vezf (GH#5005)", got[0].TargetID)
	}
	if got[1].TargetID != "fake-already-full" {
		t.Errorf("full id became %q, want unchanged", got[1].TargetID)
	}
	if got[2].TargetID != "external:other-system/42" {
		t.Errorf("external target became %q, want unchanged", got[2].TargetID)
	}
	// Types / swap flags must be preserved.
	if got[0].Type != types.DepDiscoveredFrom || got[1].Type != types.DepBlocks {
		t.Errorf("types mutated: %#v", got)
	}
}

func TestResolveDepSpecTargetsRejectsEmptyTarget(t *testing.T) {
	st := &createAtomicFakeStore{}
	_, err := resolveDepSpecTargets(context.Background(), st, []domain.DependencySpec{
		{Type: types.DepBlocks, TargetID: "  "},
	})
	if err == nil {
		t.Fatal("expected error for empty target")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty target, got: %v", err)
	}
}

func TestOverlayYAMLPrefix(t *testing.T) {
	t.Run("yaml wins when set", func(t *testing.T) {
		config.ResetForTesting()
		_ = config.Initialize()
		config.Set("issue-prefix", "yml")
		t.Cleanup(config.ResetForTesting)

		if got := overlayYAMLPrefix("dbp"); got != "yml" {
			t.Errorf("got %q, want %q", got, "yml")
		}
	})
	t.Run("db wins when yaml empty", func(t *testing.T) {
		config.ResetForTesting()
		_ = config.Initialize()
		config.Set("issue-prefix", "")
		t.Cleanup(config.ResetForTesting)

		if got := overlayYAMLPrefix("dbp"); got != "dbp" {
			t.Errorf("got %q, want %q", got, "dbp")
		}
	})
	t.Run("empty db ok when yaml empty", func(t *testing.T) {
		config.ResetForTesting()
		_ = config.Initialize()
		config.Set("issue-prefix", "")
		t.Cleanup(config.ResetForTesting)

		if got := overlayYAMLPrefix(""); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}
