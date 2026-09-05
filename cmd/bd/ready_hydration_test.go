package main

import (
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestResolveReadyProjection pins the whole decision table, because every row
// of it is a way the default can be wrong in a direction nobody would notice.
func TestResolveReadyProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		brief, full bool
		claim       bool
		gated       bool
		explain     bool
		molID       string
		jsonOut     bool
		env         map[string]string
		wantLite    bool
		wantWarning bool
	}{
		// THE CHANGE ITSELF: a plain `bd ready --json` is projected now.
		{name: "plain json defaults lite", jsonOut: true, wantLite: true},

		// THE ROLLBACK, both spellings, and they must agree.
		{name: "env full moves the default back", jsonOut: true, env: map[string]string{EnvReadyHydration: "full"}},
		{name: "env FULL is case-insensitive", jsonOut: true, env: map[string]string{EnvReadyHydration: "FULL"}},
		{name: "env lite is the explicit default", jsonOut: true, env: map[string]string{EnvReadyHydration: "lite"}, wantLite: true},
		{name: "--full beats the lite default", full: true, jsonOut: true},
		// …and --full beats the env too, in the direction the env did not go:
		// a caller that states its projection is never surprised by an
		// environment it did not set.
		{name: "--full wins over env lite", full: true, jsonOut: true, env: map[string]string{EnvReadyHydration: "lite"}},

		// AN UNREADABLE KNOB FALLS BACK TO FULL AND WARNS. The reassuring
		// failure would be to keep the lite default on a typo, leaving an
		// operator who believed they had rolled back still reading projected
		// rows.
		{name: "unrecognized env falls back to full, loudly", jsonOut: true, env: map[string]string{EnvReadyHydration: "brief"}, wantWarning: true},
		{name: "empty-ish env value is not a typo", jsonOut: true, env: map[string]string{EnvReadyHydration: "   "}, wantLite: true},

		// THE FOUR MODES REACH A DIFFERENT QUERY, so the default must not set a
		// field nothing there reads — and, far more important, must not refuse
		// them the way an explicit --brief is refused. `bd ready --claim` is
		// every claiming caller in the town.
		{name: "--claim is never defaulted", claim: true, jsonOut: true},
		{name: "--gated is never defaulted", gated: true, jsonOut: true},
		{name: "--explain is never defaulted", explain: true, jsonOut: true},
		{name: "--mol is never defaulted", molID: "bd-1", jsonOut: true},
		{name: "text output is never defaulted", jsonOut: false},

		// An explicit --brief still means what it always meant. It has already
		// passed briefModeConflict by the time this runs.
		{name: "explicit --brief stays lite", brief: true, jsonOut: true, wantLite: true},
		{name: "explicit --brief ignores an env that says full", brief: true, jsonOut: true, env: map[string]string{EnvReadyHydration: "full"}, wantLite: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lite, warning := resolveReadyProjection(tt.brief, tt.full, tt.claim, tt.gated, tt.explain, tt.molID, tt.jsonOut, envFrom(tt.env))
			if lite != tt.wantLite {
				t.Errorf("lite = %v, want %v", lite, tt.wantLite)
			}
			if (warning != "") != tt.wantWarning {
				t.Errorf("warning = %q, wantWarning = %v", warning, tt.wantWarning)
			}
		})
	}
}

// TestReadyHydrationDefaultLiteNamesTheKnobInItsWarning keeps the warning
// actionable: an operator reading it must learn the variable name and the two
// values without going to look them up.
func TestReadyHydrationDefaultLiteNamesTheKnobInItsWarning(t *testing.T) {
	t.Parallel()

	lite, warning := readyHydrationDefaultLite(envFrom(map[string]string{EnvReadyHydration: "nope"}))
	if lite {
		t.Error("an unrecognized value must fall back to FULL hydration, not to the lite default")
	}
	for _, want := range []string{EnvReadyHydration, `"lite"`, `"full"`, "nope"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not mention %q", warning, want)
		}
	}
}
