package main

import (
	"strings"
	"testing"
)

// TestIsOnlyShebangOrEmpty_TableDriven exercises the helper used by
// preservePreexistingHooks to decide, after stripping a BEADS INTEGRATION
// block, whether anything user-owned remains worth preserving. (GH#3536)
func TestIsOnlyShebangOrEmpty_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "empty", content: "", want: true},
		{name: "only blanks", content: "\n\n\n", want: true},
		{name: "only shebang", content: "#!/usr/bin/env sh\n", want: true},
		{name: "shebang + blank lines", content: "#!/bin/sh\n\n\n", want: true},
		{name: "shebang + comments", content: "#!/bin/sh\n# a comment\n# another\n", want: true},
		{name: "shebang + one command", content: "#!/bin/sh\necho hi\n", want: false},
		{name: "no shebang, has command", content: "echo hi\n", want: false},
		{name: "user dispatcher", content: "#!/bin/sh\nset -e\nrun_precommit pre-commit\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOnlyShebangOrEmpty(tt.content); got != tt.want {
				t.Errorf("isOnlyShebangOrEmpty(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// TestPreservePreexistingHooks_StripsLegacyMarkerBlock reproduces GH#3536.
// In the v0.62.x inline-injection model, bd appended its BEADS INTEGRATION
// block to the BOTTOM of an otherwise-user-owned hook. When v1.0.x tries to
// preserve that hook into .beads/hooks/<name>, the marker-presence check
// previously caused the entire file to be skipped — silently discarding the
// user's content.
//
// Expected behaviour after the fix: the bd marker block is stripped, the
// user content above it is preserved, and the new v1.0.x BEADS INTEGRATION
// block (added later by injectHookSection) re-installs the bd integration.
//
// This test exercises only the strip-decision logic — the same composition
// used inside preservePreexistingHooks.
func TestPreservePreexistingHooks_StripsLegacyMarkerBlock(t *testing.T) {
	// A v0.62.x-style file: user dispatcher above, bd marker block below.
	existing := `#!/bin/sh
set -e
# User's custom dispatcher (e.g. invokes the pre-commit framework)
if [ -f .pre-commit-config.yaml ] && command -v pre-commit >/dev/null 2>&1; then
    pre-commit run --hook-stage pre-commit "$@"
fi

# --- BEGIN BEADS INTEGRATION v0.62.0 ---
# This section is managed by beads. Do not remove these markers.
if command -v bd >/dev/null 2>&1; then
  bd hook pre-commit "$@"
fi
# --- END BEADS INTEGRATION v0.62.0 ---
`

	// 1. Old behaviour would skip on marker presence: confirm the marker IS present.
	if !strings.Contains(existing, hookSectionBeginPrefix) {
		t.Fatal("test fixture missing marker — fix the fixture")
	}

	// 2. Strip the bd section. User content must be preserved.
	stripped, found := removeHookSection(existing)
	if !found {
		t.Fatal("removeHookSection did not find a section in fixture")
	}
	if !strings.Contains(stripped, "pre-commit run --hook-stage") {
		t.Errorf("user dispatcher line lost in strip:\n%s", stripped)
	}
	if strings.Contains(stripped, "BEGIN BEADS INTEGRATION") ||
		strings.Contains(stripped, "END BEADS INTEGRATION") {
		t.Errorf("strip left bd markers in place:\n%s", stripped)
	}

	// 3. The stripped file must NOT be classified as "only shebang or empty",
	//    so preservation will keep it instead of skipping.
	if isOnlyShebangOrEmpty(stripped) {
		t.Errorf("stripped file mis-classified as empty (would be skipped):\n%s", stripped)
	}
}

// TestPreservePreexistingHooks_SkipsWhollyManagedFile guards the inverse
// case: a file that contains ONLY the bd marker block (no user content)
// genuinely is wholly bd-owned and should still be skipped — both the v1.0.x
// shim format and the bare-marker-block-with-shebang form.
func TestPreservePreexistingHooks_SkipsWhollyManagedFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "v1.0.x bare shim",
			content: `#!/usr/bin/env sh
# --- BEGIN BEADS INTEGRATION v1.0.3 ---
if command -v bd >/dev/null 2>&1; then
  bd hooks run pre-commit "$@"
fi
# --- END BEADS INTEGRATION v1.0.3 ---
`,
		},
		{
			name: "v0.62.0 marker block + nothing else",
			content: `#!/bin/sh

# --- BEGIN BEADS INTEGRATION v0.62.0 ---
# managed
if command -v bd >/dev/null 2>&1; then
  bd hook pre-commit "$@"
fi
# --- END BEADS INTEGRATION v0.62.0 ---
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, found := removeHookSection(tt.content)
			if !found {
				t.Fatal("removeHookSection did not find a section")
			}
			if !isOnlyShebangOrEmpty(stripped) {
				t.Errorf("expected stripped content to be classified as empty (preservation should skip), got:\n%q", stripped)
			}
		})
	}
}

// TestPreservePreexistingHooks_LeavesNonMarkerHookAlone is the regression
// guard: a file with no bd markers must not be touched by the new
// strip-then-preserve logic.
func TestPreservePreexistingHooks_LeavesNonMarkerHookAlone(t *testing.T) {
	content := `#!/bin/sh
set -e
echo "user pre-commit"
make lint
`
	if strings.Contains(content, hookSectionBeginPrefix) {
		t.Fatal("test fixture contains bd marker — adjust fixture")
	}
	// removeHookSection on a no-marker file should report nothing found and
	// return content unchanged.
	stripped, found := removeHookSection(content)
	if found {
		t.Errorf("unexpected: removeHookSection reported found on a no-marker file")
	}
	if stripped != content {
		t.Errorf("removeHookSection mutated a no-marker file:\nbefore=%q\nafter =%q", content, stripped)
	}
}
