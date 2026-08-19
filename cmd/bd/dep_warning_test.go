package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestWarnImplicitBlocksDefault is the D1 guard test: a dep add edge created
// with the implicit type=blocks default (no -t/--type) must warn on stderr
// that the edge is type=blocks and that structural parent/child linkage
// requires -t parent-child, so children don't silently drop from bd ready.
func TestWarnImplicitBlocksDefault(t *testing.T) {
	tests := []struct {
		name     string
		dt       types.DependencyType
		flagSet  bool
		wantWarn bool
	}{
		{name: "implicit blocks default warns", dt: types.DepBlocks, flagSet: false, wantWarn: true},
		{name: "explicit blocks flag does not warn", dt: types.DepBlocks, flagSet: true, wantWarn: false},
		{name: "parent-child default does not warn", dt: types.DepParentChild, flagSet: false, wantWarn: false},
		{name: "tracks default does not warn", dt: types.DepTracks, flagSet: false, wantWarn: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureStderr(t, func() {
				warnImplicitBlocksDefault(tt.dt, tt.flagSet)
			})

			if tt.wantWarn {
				if !strings.Contains(got, "type=blocks") {
					t.Errorf("warning must state the edge is type=blocks, got %q", got)
				}
				if !strings.Contains(got, "-t parent-child") {
					t.Errorf("warning must name -t parent-child for structural linkage, got %q", got)
				}
				if !strings.Contains(got, "bd ready") {
					t.Errorf("warning must explain the bd ready impact, got %q", got)
				}
			} else if got != "" {
				t.Errorf("expected no warning, got %q", got)
			}
		})
	}
}
