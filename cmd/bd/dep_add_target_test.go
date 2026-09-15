package main

import (
	"errors"
	"strings"
	"testing"
)

// resolveUnresolvedDepTarget is the decision point `bd dep add` reaches when
// its depends-on target could not be resolved locally or via cross-store
// routing (resolveIDWithRouting's error return). Before the fix for
// bd-gmdx5, dep add fell back to comparing bd ID prefixes
// (types.ExtractPrefix) between the source and target IDs, and silently
// accepted ANY differently-prefixed string as a raw external reference —
// with no validation and no error. That one fallback branch produces two bug
// shapes from one root cause:
//
//  1. a bare foreign-looking bd ID (e.g. "xy-999") gets silently written to
//     depends_on_external even though it was never actually resolvable or
//     routable anywhere.
//  2. a "type:id"-shaped positional argument (e.g. "discovered-from:ga-dguvnb",
//     the syntax `bd create --deps` accepts via parseDepSpec) gets misparsed:
//     types.ExtractPrefix takes everything up to the first "-", so it reads
//     "discovered-" as a bd ID prefix instead of recognizing "discovered-from"
//     as a dependency-type keyword. The intended type is silently dropped
//     (defaults to blocks) and the whole "type:id" string is stored as a
//     bogus external reference.
//
// The fix requires any target that fails local/routed resolution to be a
// well-formed "external:<project>:<capability>" reference (validateExternalRef)
// or be rejected outright, naming the target that could not be resolved —
// never a silent raw passthrough.
func TestResolveUnresolvedDepTarget(t *testing.T) {
	resolveErr := errors.New("issue not found")

	tests := []struct {
		name            string
		dependsOnArg    string
		wantToID        string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:            "bare foreign-looking bd ID is rejected, not silently passed through",
			dependsOnArg:    "xy-999",
			wantErr:         true,
			wantErrContains: "xy-999",
		},
		{
			name:            "type:id-shaped positional arg is rejected, not silently misparsed",
			dependsOnArg:    "discovered-from:ga-dguvnb",
			wantErr:         true,
			wantErrContains: "discovered-from:ga-dguvnb",
		},
		{
			name:         "well-formed external ref is still accepted",
			dependsOnArg: "external:otherproj:some-capability",
			wantToID:     "external:otherproj:some-capability",
			wantErr:      false,
		},
		{
			name:            "malformed external-looking ref missing capability is rejected",
			dependsOnArg:    "external:otherproj",
			wantErr:         true,
			wantErrContains: "external:otherproj",
		},
		{
			name:         "empty target is rejected",
			dependsOnArg: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toID, err := resolveUnresolvedDepTarget(tt.dependsOnArg, resolveErr)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveUnresolvedDepTarget(%q): got nil error, want error naming the unresolved target", tt.dependsOnArg)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("resolveUnresolvedDepTarget(%q): error %q does not name the unresolved target %q", tt.dependsOnArg, err.Error(), tt.wantErrContains)
				}
				if toID != "" {
					t.Errorf("resolveUnresolvedDepTarget(%q): got toID %q on error, want empty", tt.dependsOnArg, toID)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q): unexpected error: %v", tt.dependsOnArg, err)
			}
			if toID != tt.wantToID {
				t.Errorf("resolveUnresolvedDepTarget(%q) = %q, want %q", tt.dependsOnArg, toID, tt.wantToID)
			}
		})
	}
}
