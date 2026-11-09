package doctor

import (
	"testing"
)

// TestGitignoreIgnoresLegacyProxiedRootFile pins the pattern shape the
// legacy proxied root guard (GH#5104) depends on. The guard's marker at
// .beads/proxieddb is a REGULAR FILE, and a directory-only `proxieddb/`
// pattern does not ignore a regular file — only the standalone `proxieddb`
// pattern does. The template (what a new init writes) and requiredPatterns
// (what doctor's append-only repair enforces) must both carry the standalone
// form, or the guard would write a marker git offers to commit.
func TestGitignoreIgnoresLegacyProxiedRootFile(t *testing.T) {
	if !containsGitignorePattern(GitignoreTemplate, "proxieddb") {
		t.Fatal("GitignoreTemplate lacks the standalone proxieddb pattern: the directory-only proxieddb/ form does not ignore the guard's regular marker file")
	}
	for _, pattern := range requiredPatterns {
		if pattern == "proxieddb" {
			return
		}
	}
	t.Fatal("requiredPatterns lacks the standalone proxieddb pattern: doctor's repair would not append the one form that ignores the guard's regular marker file")
}
