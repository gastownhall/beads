package main

import (
	"strings"
	"testing"
)

// TestIsOnlyShebangOrEmpty_TableDriven exercises the helper used by
// shouldPreserveHookContent to decide, after stripping a BEADS INTEGRATION
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

// TestShouldPreserveHookContent_TableDriven covers every branch of the new
// pure-function decision used by preservePreexistingHooks. Each case mirrors
// a real situation the migration path encounters. (GH#3536)
func TestShouldPreserveHookContent_TableDriven(t *testing.T) {
	const v062Marker = "# --- BEGIN BEADS INTEGRATION v0.62.0 ---\n" +
		"# This section is managed by beads. Do not remove these markers.\n" +
		"if command -v bd >/dev/null 2>&1; then\n" +
		"  bd hook pre-commit \"$@\"\n" +
		"fi\n" +
		"# --- END BEADS INTEGRATION v0.62.0 ---\n"

	tests := []struct {
		name      string
		content   string
		fromHusky bool
		wantKeep  bool
		wantBody  string // exact wantBody when wantKeep is true; ignored otherwise
		wantNot   string // substring that must NOT be present in the kept body
	}{
		{
			name:     "inline marker — wholly bd-managed, skip",
			content:  "#!/bin/sh\n# bd (beads)\nbd hook pre-commit \"$@\"\n",
			wantKeep: false,
		},
		{
			name: "v0.62.x style: dispatcher above + bd marker block — strip and preserve",
			content: "#!/bin/sh\nset -e\n" +
				"# user dispatcher\n" +
				"if [ -f .pre-commit-config.yaml ] && command -v pre-commit >/dev/null 2>&1; then\n" +
				"    pre-commit run --hook-stage pre-commit \"$@\"\n" +
				"fi\n\n" +
				v062Marker,
			wantKeep: true,
			wantNot:  "BEADS INTEGRATION",
		},
		{
			name:     "marker block + only shebang — strip leaves nothing, skip",
			content:  "#!/bin/sh\n\n" + v062Marker,
			wantKeep: false,
		},
		{
			name:     "no markers — preserve verbatim",
			content:  "#!/bin/sh\nset -e\necho 'plain user hook'\nmake lint\n",
			wantKeep: true,
			wantBody: "#!/bin/sh\nset -e\necho 'plain user hook'\nmake lint\n",
		},
		{
			name:      "husky v8: source line stripped, PATH injected",
			content:   "#!/usr/bin/env sh\n. \"$(dirname -- \"$0\")/_/husky.sh\"\n\nnpx lint-staged\n",
			fromHusky: true,
			wantKeep:  true,
			wantNot:   "/_/husky.sh",
		},
		{
			name:      "husky after marker-strip: still gets sanitized",
			content:   "#!/usr/bin/env sh\n. \"$(dirname -- \"$0\")/_/husky.sh\"\n\nnpx lint-staged\n\n" + v062Marker,
			fromHusky: true,
			wantKeep:  true,
			wantNot:   "BEADS INTEGRATION",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, keep := shouldPreserveHookContent(tt.content, tt.fromHusky)
			if keep != tt.wantKeep {
				t.Fatalf("keep = %v, want %v (body=%q)", keep, tt.wantKeep, body)
			}
			if !tt.wantKeep {
				return
			}
			if tt.wantBody != "" && body != tt.wantBody {
				t.Errorf("body mismatch:\nwant=%q\ngot =%q", tt.wantBody, body)
			}
			if tt.wantNot != "" && strings.Contains(body, tt.wantNot) {
				t.Errorf("kept body unexpectedly contains %q:\n%s", tt.wantNot, body)
			}
		})
	}
}

// TestShouldPreserveHookContent_TwoBlankLinesBeforeMarker exercises the
// boundary where user content ends in two blank lines before the bd marker
// block. removeHookSection collapses runs of `\n\n\n` to `\n\n`, so this
// fixture confirms the strip doesn't over-trim user content.
func TestShouldPreserveHookContent_TwoBlankLinesBeforeMarker(t *testing.T) {
	in := "#!/bin/sh\n" +
		"set -e\n" +
		"echo 'user line 1'\n" +
		"echo 'user line 2'\n" +
		"\n" +
		"\n" +
		"# --- BEGIN BEADS INTEGRATION v0.62.0 ---\n" +
		"if command -v bd >/dev/null 2>&1; then bd hook pre-commit \"$@\"; fi\n" +
		"# --- END BEADS INTEGRATION v0.62.0 ---\n"

	body, keep := shouldPreserveHookContent(in, false)
	if !keep {
		t.Fatalf("two-blank-line boundary: keep=false, body=%q", body)
	}
	if !strings.Contains(body, "echo 'user line 1'") || !strings.Contains(body, "echo 'user line 2'") {
		t.Errorf("user content lost across two-blank-line boundary:\n%q", body)
	}
	if strings.Contains(body, "BEADS INTEGRATION") {
		t.Errorf("bd markers leaked through:\n%q", body)
	}
}

// TestShouldPreserveHookContent_CRLFNormalised verifies that a v0.62.x hook
// stored with Windows-style `\r\n` line endings is normalised to LF on the
// preserved-and-stripped output. Mirrors the normalisation that
// injectHookSection applies on its inject path. (GH#3536 nit)
func TestShouldPreserveHookContent_CRLFNormalised(t *testing.T) {
	in := "#!/bin/sh\r\n" +
		"set -e\r\n" +
		"echo 'user'\r\n" +
		"\r\n" +
		"# --- BEGIN BEADS INTEGRATION v0.62.0 ---\r\n" +
		"if command -v bd >/dev/null 2>&1; then bd hook pre-commit \"$@\"; fi\r\n" +
		"# --- END BEADS INTEGRATION v0.62.0 ---\r\n"

	body, keep := shouldPreserveHookContent(in, false)
	if !keep {
		t.Fatalf("CRLF input: keep=false, body=%q", body)
	}
	if strings.Contains(body, "\r\n") {
		t.Errorf("CRLF survived into preserved output (should be normalised to LF):\n%q", body)
	}
	if !strings.Contains(body, "echo 'user'") {
		t.Errorf("user content lost:\n%q", body)
	}
}

// TestShouldPreserveHookContent_StrippedDispatcherSurvives is the explicit
// GH#3536 regression: the user dispatcher above the bd marker block must
// remain in the returned body so preservePreexistingHooks writes it into
// .beads/hooks/<name>.
func TestShouldPreserveHookContent_StrippedDispatcherSurvives(t *testing.T) {
	in := `#!/bin/sh
set -e
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
	body, keep := shouldPreserveHookContent(in, false)
	if !keep {
		t.Fatalf("dispatcher with marker block: keep=false, body=%q", body)
	}
	if !strings.Contains(body, "pre-commit run --hook-stage") {
		t.Errorf("dispatcher line lost:\n%s", body)
	}
	if strings.Contains(body, "BEGIN BEADS INTEGRATION") ||
		strings.Contains(body, "END BEADS INTEGRATION") {
		t.Errorf("bd markers leaked through:\n%s", body)
	}
	if strings.Contains(body, "bd hook pre-commit") {
		t.Errorf("legacy 'bd hook' invocation should have been stripped with the marker block:\n%s", body)
	}
}
