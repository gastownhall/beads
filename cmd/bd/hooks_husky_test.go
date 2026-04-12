package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSanitizeHuskyHook_V8 verifies that husky v8's `_/husky.sh` source line
// is stripped and replaced with a PATH export, so the hook runs standalone
// without needing `.beads/hooks/_/husky.sh` to exist. (GH#3132)
func TestSanitizeHuskyHook_V8(t *testing.T) {
	in := `#!/usr/bin/env sh
. "$(dirname -- "$0")/_/husky.sh"

npx lint-staged
`
	out := sanitizeHuskyHook(in)

	if strings.Contains(out, "_/husky.sh") {
		t.Errorf("expected husky.sh source line stripped, got:\n%s", out)
	}
	if !strings.Contains(out, "npx lint-staged") {
		t.Errorf("expected user commands preserved, got:\n%s", out)
	}
	if !strings.Contains(out, `export PATH="$PWD/node_modules/.bin:$PATH"`) {
		t.Errorf("expected PATH export injected, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "#!/usr/bin/env sh") {
		t.Errorf("expected shebang preserved at top, got:\n%s", out)
	}
}

// TestSanitizeHuskyHook_V9 verifies that husky v9's `. "$(dirname "$0")/h"`
// dispatcher-source line is stripped. (GH#3132)
func TestSanitizeHuskyHook_V9(t *testing.T) {
	in := `#!/usr/bin/env sh
. "$(dirname "$0")/h"

npm run minify-templates
npx lint-staged --allow-empty
`
	out := sanitizeHuskyHook(in)

	if strings.Contains(out, `/h"`) && strings.Contains(out, "dirname") {
		// Both markers present on the same line would mean we didn't strip.
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "dirname") && strings.Contains(line, `/h"`) {
				t.Errorf("expected v9 dispatcher source line stripped, found: %q", line)
			}
		}
	}
	if !strings.Contains(out, "npm run minify-templates") {
		t.Errorf("expected user commands preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "npx lint-staged --allow-empty") {
		t.Errorf("expected user commands preserved, got:\n%s", out)
	}
	if !strings.Contains(out, `export PATH="$PWD/node_modules/.bin:$PATH"`) {
		t.Errorf("expected PATH export injected for v9, got:\n%s", out)
	}
}

// TestSanitizeHuskyHook_NonHusky verifies we don't mangle hooks that don't
// look like husky hooks.
func TestSanitizeHuskyHook_NonHusky(t *testing.T) {
	in := `#!/bin/sh
echo "hello"
make test
`
	out := sanitizeHuskyHook(in)
	if out != in {
		t.Errorf("non-husky hook should be returned unchanged.\ninput:\n%s\noutput:\n%s", in, out)
	}
}

// TestIsHuskyHelperSourceLine checks the matcher for both husky versions.
func TestIsHuskyHelperSourceLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{`. "$(dirname -- "$0")/_/husky.sh"`, true},
		{`. "$(dirname "$0")/_/husky.sh"`, true},
		{`source "$(dirname "$0")/_/husky.sh"`, true},
		{`. "$(dirname "$0")/h"`, true},
		{`. "$(dirname -- "$0")/h"`, true},
		// Shouldn't match:
		{`echo hello`, false},
		{`. ./config.sh`, false},
		{`# . "$(dirname "$0")/h"`, false}, // commented out (doesn't start with `. `)
		{``, false},
	}
	for _, tc := range tests {
		got := isHuskyHelperSourceLine(strings.TrimSpace(tc.line))
		if got != tc.want {
			t.Errorf("isHuskyHelperSourceLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestIsHuskyDir verifies .husky and .husky/_ detection.
func TestIsHuskyDir(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/repo/.husky", true},
		{"/repo/.husky/_", true},
		{".husky", true},
		{"/repo/.git/hooks", false},
		{"/repo/.beads/hooks", false},
		{"", false},
	}
	for _, tc := range tests {
		got := isHuskyDir(tc.path)
		if got != tc.want {
			t.Errorf("isHuskyDir(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestSanitizeHuskyHook_EndToEnd simulates the file write path: write a
// husky-style hook, sanitize it, write the result, and verify the written
// file would execute standalone. (We don't actually exec — just assert the
// content no longer references the husky helpers.)
func TestSanitizeHuskyHook_EndToEnd(t *testing.T) {
	tmp := t.TempDir()
	huskyDir := filepath.Join(tmp, ".husky")
	if err := os.MkdirAll(huskyDir, 0755); err != nil {
		t.Fatal(err)
	}

	huskyHook := `#!/usr/bin/env sh
. "$(dirname -- "$0")/_/husky.sh"

npx --no-install lint-staged
`
	srcPath := filepath.Join(huskyDir, "pre-commit")
	// #nosec G306 - test file
	if err := os.WriteFile(srcPath, []byte(huskyHook), 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate what preservePreexistingHooks does: read, sanitize, write.
	content, err := os.ReadFile(srcPath) // #nosec G304 - test path
	if err != nil {
		t.Fatal(err)
	}
	sanitized := content
	if isHuskyDir(huskyDir) {
		sanitized = []byte(sanitizeHuskyHook(string(content)))
	}

	targetDir := filepath.Join(tmp, ".beads", "hooks")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	dstPath := filepath.Join(targetDir, "pre-commit")
	// #nosec G306 - test file
	if err := os.WriteFile(dstPath, sanitized, 0755); err != nil {
		t.Fatal(err)
	}

	// Verify written content.
	written, err := os.ReadFile(dstPath) // #nosec G304 - test path
	if err != nil {
		t.Fatal(err)
	}
	got := string(written)
	if strings.Contains(got, "_/husky.sh") {
		t.Errorf("preserved hook still references _/husky.sh; would break commits.\n%s", got)
	}
	if !strings.Contains(got, "lint-staged") {
		t.Errorf("preserved hook lost user command.\n%s", got)
	}
}
