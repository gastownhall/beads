//go:build cgo

package utils_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// TestResolvePartialIDExact_RejectsAbbreviationAcceptsExact reuses the same
// fixture shape as TestResolvePartialID (full/partial/hierarchical/substring
// issues) to show ResolvePartialIDExact keeps every exact-match case working
// identically to ResolvePartialID, while every leading-prefix-abbreviation
// case — which ResolvePartialID happily resolves — now errors instead of
// silently picking a candidate.
func TestResolvePartialIDExact_RejectsAbbreviationAcceptsExact(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	prefixIssue := &types.Issue{
		ID:        "bd-a3f8e9",
		Title:     "Prefix resolution target",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	plainIssue := &types.Issue{
		ID:        "bd-1",
		Title:     "Plain issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}

	if err := store.CreateIssue(ctx, prefixIssue, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIssue(ctx, plainIssue, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		input       string
		expected    string
		shouldError bool
	}{
		{
			name:     "exact match with prefix still resolves",
			input:    "bd-1",
			expected: "bd-1",
		},
		{
			name:     "exact match without prefix still resolves",
			input:    "1",
			expected: "bd-1",
		},
		{
			name:     "exact full hash still resolves",
			input:    "a3f8e9",
			expected: "bd-a3f8e9",
		},
		{
			name:        "leading-prefix abbreviation now errors (was silently accepted by ResolvePartialID)",
			input:       "a3f8",
			shouldError: true,
		},
		{
			name:        "nonexistent issue still errors",
			input:       "bd-999",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := utils.ResolvePartialIDExact(ctx, store, tt.input)
			if tt.shouldError {
				if err == nil {
					t.Fatalf("utils.ResolvePartialIDExact(%q) = %q, nil; want error", tt.input, result)
				}
				return
			}
			if err != nil {
				t.Fatalf("utils.ResolvePartialIDExact(%q) unexpected error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("utils.ResolvePartialIDExact(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestResolvePartialIDExact_Wisp is a direct regression test for the
// reported incident: a wisp's stripped hash ("t3st" from "bd-wisp-t3st")
// must still resolve exactly (that's a real, deliberate exact-match
// convenience — referencing a wisp without typing "wisp-"), but a
// leading-prefix abbreviation of that hash ("t3s", or the real incident's
// "list" against "list3t0") must now error instead of silently landing on
// the wisp.
func TestResolvePartialIDExact_Wisp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	wisp := &types.Issue{
		ID:        "bd-wisp-t3st",
		Title:     "Test wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		input       string
		expected    string
		shouldError bool
	}{
		{
			name:     "full wisp ID still resolves exactly",
			input:    "bd-wisp-t3st",
			expected: "bd-wisp-t3st",
		},
		{
			name:     "wisp prefix with full hash still resolves exactly",
			input:    "wisp-t3st",
			expected: "bd-wisp-t3st",
		},
		{
			name:     "bare full hash still resolves exactly (wisp- infix stripped, but hash is complete)",
			input:    "t3st",
			expected: "bd-wisp-t3st",
		},
		{
			name:        "leading-prefix abbreviation of the wisp hash now errors",
			input:       "t3s",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := utils.ResolvePartialIDExact(ctx, store, tt.input)
			if tt.shouldError {
				if err == nil {
					t.Fatalf("utils.ResolvePartialIDExact(%q) = %q, nil; want error", tt.input, result)
				}
				return
			}
			if err != nil {
				t.Fatalf("utils.ResolvePartialIDExact(%q) unexpected error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("utils.ResolvePartialIDExact(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestResolvePartialIDExact_ReservedWordDoesNotCollideWithWisp is the exact
// shape of the real incident: a wisp whose hash happens to start with a
// word a caller might type by accident. Even without the CLI-level
// reserved-word guard, ResolvePartialIDExact alone must refuse to resolve
// "list" against a wisp hashed "list3t0".
func TestResolvePartialIDExact_ReservedWordDoesNotCollideWithWisp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	wisp := &types.Issue{
		ID:        "bd-wisp-list3t0",
		Title:     "Unrelated wisp (order:dolt-health)",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatal(err)
	}

	if result, err := utils.ResolvePartialIDExact(ctx, store, "list"); err == nil {
		t.Fatalf(`utils.ResolvePartialIDExact("list") = %q, nil; want "not found" error, not a silent match onto %s`, result, wisp.ID)
	}

	// Sanity check against the OLD behavior: ResolvePartialID (still the
	// default for read paths) is documented to resolve this via the leading-
	// prefix abbreviation branch — confirms the fixture actually reproduces
	// the incident's collision instead of trivially not matching anything.
	if result, err := utils.ResolvePartialID(ctx, store, "list"); err != nil || result != wisp.ID {
		t.Fatalf("fixture sanity check failed: utils.ResolvePartialID(%q) = (%q, %v); want (%q, nil) — the incident's collision did not reproduce", "list", result, err, wisp.ID)
	}
}
