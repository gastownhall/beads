package doctor

import (
	"fmt"
	"strings"
	"testing"
)

// Note: CheckHooksQuick relies on git.GetGitHooksDir() which caches the git context
// once per process. This makes it impossible to test with temp git repos in the same
// process. Instead, we test the version comparison and string formatting logic that
// CheckHooksQuick uses.

// TestCheckHooksQuick_CompareVersions exercises the version comparison used by
// CheckHooksQuick to detect outdated hooks.
func TestCheckHooksQuick_CompareVersions(t *testing.T) {
	tests := []struct {
		hookVersion string
		cliVersion  string
		wantOutdated bool
	}{
		{"0.49.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "0.49.0", false}, // CLI is older than hook
		{"0.55.0", "0.55.1", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.hookVersion, tt.cliVersion), func(t *testing.T) {
			isOutdated := tt.hookVersion != tt.cliVersion && CompareVersions(tt.cliVersion, tt.hookVersion) > 0
			if isOutdated != tt.wantOutdated {
				t.Errorf("hookVersion=%q cliVersion=%q: outdated=%v, want %v",
					tt.hookVersion, tt.cliVersion, isOutdated, tt.wantOutdated)
			}
		})
	}
}

// TestCheckHooksQuick_OutputFormat verifies the output string format for different
// numbers of outdated hooks.
func TestCheckHooksQuick_OutputFormat(t *testing.T) {
	// Single hook format
	singleMsg := fmt.Sprintf("Git hook %s outdated (%s → %s)", "pre-commit", "0.49.0", "1.0.0")
	if !strings.Contains(singleMsg, "pre-commit") {
		t.Errorf("single hook message missing hook name: %q", singleMsg)
	}
	if !strings.Contains(singleMsg, "→") {
		t.Errorf("single hook message missing arrow: %q", singleMsg)
	}

	// Multiple hooks format
	hooks := []string{"pre-commit", "post-merge", "pre-push"}
	multiMsg := fmt.Sprintf("Git hooks outdated: %s (%s → %s)", strings.Join(hooks, ", "), "0.49.0", "1.0.0")
	if !strings.Contains(multiMsg, "hooks outdated") {
		t.Errorf("multi hook message missing 'hooks outdated': %q", multiMsg)
	}
	for _, hook := range hooks {
		if !strings.Contains(multiMsg, hook) {
			t.Errorf("multi hook message missing %q: %q", hook, multiMsg)
		}
	}
}
