package main

import (
	"errors"
	"strings"
	"testing"

	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// resolveUnresolvedDepTarget is the decision point `bd dep add` reaches when
// its depends-on target could not be resolved locally or via cross-store
// routing (resolveIDWithRouting's error return). It has to separate two things
// the old prefix-comparison heuristic conflated:
//
//   - A "type:id"-shaped positional argument (e.g. "discovered-from:ga-dguvnb",
//     the syntax `bd create --deps` accepts via parseDepSpec) was misparsed:
//     types.ExtractPrefix takes everything up to the first "-", so it read
//     "discovered-" as a bd ID prefix instead of recognizing "discovered-from"
//     as a dependency-type keyword. The intended type was silently dropped
//     (defaulting to blocks) and the whole "type:id" string was stored as a
//     bogus external reference. That is bd-gmdx5, and it is a real bug.
//
//   - A bare, differently-prefixed target (e.g. "xy-999" from a "be-" issue) is
//     NOT a bug. storeissueops.IsExternalDepTarget defines exactly this shape as
//     belonging in depends_on_external, and calls that the single rule every
//     backend classifies by. It is the multi-rig "add now, route later" shape,
//     and `bd dep remove` still addresses such an edge. dep add must keep
//     accepting it, or it would refuse to create an edge the store holds and
//     dep remove can still delete.
func TestResolveUnresolvedDepTarget(t *testing.T) {
	resolveErr := errors.New("issue not found")

	tests := []struct {
		name            string
		sourceID        string
		dependsOnArg    string
		wantToID        string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:         "bare cross-prefix target is passed through as a cross-store edge",
			sourceID:     "be-abc",
			dependsOnArg: "xy-999",
			wantToID:     "xy-999",
			wantErr:      false,
		},
		{
			name:            "type:id-shaped positional arg is rejected, not silently misparsed",
			sourceID:        "be-abc",
			dependsOnArg:    "discovered-from:ga-dguvnb",
			wantErr:         true,
			wantErrContains: "discovered-from:ga-dguvnb",
		},
		{
			name:            "type:id rejection names the flag form that would have worked",
			sourceID:        "be-abc",
			dependsOnArg:    "discovered-from:ga-dguvnb",
			wantErr:         true,
			wantErrContains: "bd dep add be-abc ga-dguvnb --type discovered-from",
		},
		{
			name:         "well-formed external ref is still accepted",
			sourceID:     "be-abc",
			dependsOnArg: "external:otherproj:some-capability",
			wantToID:     "external:otherproj:some-capability",
			wantErr:      false,
		},
		{
			name:            "malformed external-looking ref missing capability is rejected",
			sourceID:        "be-abc",
			dependsOnArg:    "external:otherproj",
			wantErr:         true,
			wantErrContains: "external:otherproj",
		},
		{
			name:            "same-prefix target that resolves nowhere is still an error",
			sourceID:        "be-abc",
			dependsOnArg:    "be-nosuchbead",
			wantErr:         true,
			wantErrContains: "be-nosuchbead",
		},
		{
			name:            "target with no prefix at all is rejected",
			sourceID:        "be-abc",
			dependsOnArg:    "nodashhere",
			wantErr:         true,
			wantErrContains: "nodashhere",
		},
		{
			name:         "empty target is rejected",
			sourceID:     "be-abc",
			dependsOnArg: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toID, err := resolveUnresolvedDepTarget(tt.sourceID, tt.dependsOnArg, resolveErr)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveUnresolvedDepTarget(%q, %q): got nil error, want error naming the unresolved target", tt.sourceID, tt.dependsOnArg)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): error %q does not contain %q", tt.sourceID, tt.dependsOnArg, err.Error(), tt.wantErrContains)
				}
				if toID != "" {
					t.Errorf("resolveUnresolvedDepTarget(%q, %q): got toID %q on error, want empty", tt.sourceID, tt.dependsOnArg, toID)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", tt.sourceID, tt.dependsOnArg, err)
			}
			if toID != tt.wantToID {
				t.Errorf("resolveUnresolvedDepTarget(%q, %q) = %q, want %q", tt.sourceID, tt.dependsOnArg, toID, tt.wantToID)
			}
		})
	}
}

// TestResolveUnresolvedDepTargetMatchesStorageClassification pins the CLI's
// accept/refuse decision to the storage layer's own definition of an external
// dependency target. Every target dep add passes through here must be one
// IsExternalDepTarget classifies as external — i.e. one depends_on_external can
// actually hold. If these two ever disagree, dep add is writing edges a backend
// will route to the wrong column (or refusing edges it would have held fine),
// which is the failure this whole change is about.
func TestResolveUnresolvedDepTargetMatchesStorageClassification(t *testing.T) {
	resolveErr := errors.New("issue not found")

	const sourceID = "be-abc"
	accepted := []string{
		"xy-999",
		"external:otherproj:some-capability",
	}

	for _, target := range accepted {
		t.Run(target, func(t *testing.T) {
			toID, err := resolveUnresolvedDepTarget(sourceID, target, resolveErr)
			if err != nil {
				t.Fatalf("resolveUnresolvedDepTarget(%q, %q): unexpected error: %v", sourceID, target, err)
			}
			if !storeissueops.IsExternalDepTarget(sourceID, toID) {
				t.Errorf("dep add accepted %q but storage would not classify it as an external dep target; "+
					"the edge would be written to a column that cannot hold it", toID)
			}
		})
	}
}
