package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestShouldWarnImplicitBlocksDefault is the D1 guard test: a dep add edge
// created with the implicit type=blocks default must warn on stderr that the
// edge is type=blocks and that structural parent/child linkage requires -t
// parent-child, so children don't silently drop from bd ready. At the command
// layer, explicit is true when the user passed -t (any value, including
// blocks) or the --blocked-by/--depends-on aliases.
//
// The warning fires on the documented-default majority path, so it is scoped
// to an interactive operator: a non-TTY stderr (scripted and agent callers,
// CI, `bd dep add 2>log`), --quiet, and BD_NO_DEP_TYPE_WARNING each suppress
// it. Those three cases are the anti-inversion controls — without them the
// warning would train operators and agents to ignore stderr on the correct
// path.
func TestShouldWarnImplicitBlocksDefault(t *testing.T) {
	tests := []struct {
		name             string
		dt               types.DependencyType
		explicit         bool
		quiet            bool
		noWarnEnv        string
		stderrIsTerminal bool
		want             bool
	}{
		{name: "implicit blocks default on a TTY warns", dt: types.DepBlocks, stderrIsTerminal: true, want: true},
		{name: "explicit blocks (-t or --blocked-by/--depends-on) does not warn", dt: types.DepBlocks, explicit: true, stderrIsTerminal: true, want: false},
		{name: "parent-child default does not warn", dt: types.DepParentChild, stderrIsTerminal: true, want: false},
		{name: "tracks default does not warn", dt: types.DepTracks, stderrIsTerminal: true, want: false},
		{name: "non-TTY stderr does not warn (scripted and agent callers)", dt: types.DepBlocks, stderrIsTerminal: false, want: false},
		{name: "--quiet does not warn", dt: types.DepBlocks, quiet: true, stderrIsTerminal: true, want: false},
		{name: "BD_NO_DEP_TYPE_WARNING does not warn", dt: types.DepBlocks, noWarnEnv: "1", stderrIsTerminal: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldWarnImplicitBlocksDefault(tt.dt, tt.explicit, tt.quiet, tt.noWarnEnv, tt.stderrIsTerminal)
			if got != tt.want {
				t.Errorf("shouldWarnImplicitBlocksDefault(%v, explicit=%v, quiet=%v, env=%q, tty=%v) = %v, want %v",
					tt.dt, tt.explicit, tt.quiet, tt.noWarnEnv, tt.stderrIsTerminal, got, tt.want)
			}
		})
	}
}

// TestEmitImplicitBlocksDefaultWarning locks the message content. It calls the
// emitter directly because captureStderr replaces stderr with a pipe, which is
// exactly the non-TTY case the gate above suppresses.
func TestEmitImplicitBlocksDefaultWarning(t *testing.T) {
	got := captureStderr(t, emitImplicitBlocksDefaultWarning)

	if !strings.Contains(got, "type=blocks") {
		t.Errorf("warning must state the edge is type=blocks, got %q", got)
	}
	if !strings.Contains(got, "-t parent-child") {
		t.Errorf("warning must name -t parent-child for structural linkage, got %q", got)
	}
	if !strings.Contains(got, "bd ready") {
		t.Errorf("warning must explain the bd ready impact, got %q", got)
	}
	if !strings.Contains(got, "--quiet") || !strings.Contains(got, "BD_NO_DEP_TYPE_WARNING") {
		t.Errorf("warning must name both suppression knobs, got %q", got)
	}
}

// TestWarnImplicitBlocksDefaultStaysSilentUnderCapturedStderr is the
// end-to-end control: the command-layer entry point must print nothing when
// stderr is not a terminal, which is every scripted and agent invocation.
func TestWarnImplicitBlocksDefaultStaysSilentUnderCapturedStderr(t *testing.T) {
	got := captureStderr(t, func() {
		warnImplicitBlocksDefault(types.DepBlocks, false)
	})
	if got != "" {
		t.Errorf("expected no warning when stderr is not a terminal, got %q", got)
	}
}
