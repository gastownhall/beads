package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A hand-composed hook that bd never installed, which mentions beads because it
// chains to bd among other guards. This is the shape the old heuristic deleted:
// it asked whether any of the first ten lines contained the substring "beads",
// which this does, on a file bd has no claim to at all.
const composedForeignHook = `#!/usr/bin/env bash
# pre-commit — repo guard.
#
# Composition: this is the umbrella pre-commit hook. It chains to the
# project's own config guard, then runs the beads hook, then applies the
# worktree check. bd did not write this file and must not remove it.
set -euo pipefail

./scripts/check-config
bd hooks run pre-commit "$@"
./scripts/worktree-guard
`

func writeHook(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { // #nosec G306 -- hooks are executable
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestClassifyResetHook(t *testing.T) {
	section := generateHookSection("pre-commit")

	tests := []struct {
		name    string
		content string
		want    hookOwnership
	}{
		{
			name:    "inline hook written by bd init",
			content: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			want:    hookBdOwned,
		},
		{
			name:    "legacy shim written by bd",
			content: "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n",
			want:    hookBdOwned,
		},
		{
			name:    "section-only file: bd wrote everything but the shebang",
			content: "#!/bin/sh\n" + section,
			want:    hookBdOwned,
		},
		{
			name:    "user hook with bd's section injected",
			content: "#!/bin/sh\nset -e\n./scripts/lint\n\n" + section,
			want:    hookUserOwnedWithBdSection,
		},
		{
			// The user's own file: a header they wrote and a note to whoever
			// edits it next, with bd's block injected below. Stripping the block
			// leaves only comments, which shouldPreserveHookContent is entitled
			// to call nothing — it is deciding whether to COPY the file forward.
			// Reset is deciding whether to DELETE it, and has no backup to
			// restore, so comments are content here.
			name: "user hook whose own content is only comments",
			content: "#!/bin/sh\n" +
				"# pre-commit — team hook.\n" +
				"# TODO(ana): re-enable the lint step once CI stops flaking.\n" +
				"# ./scripts/lint\n\n" + section,
			want: hookUserOwnedWithBdSection,
		},
		{
			// Same shape, but the comment sits below bd's section rather than
			// above it: position must not decide whether the file is the user's.
			name:    "user comment below bd's section",
			content: "#!/bin/sh\n" + section + "\n# leave this hook alone, see docs/hooks.md\n",
			want:    hookUserOwnedWithBdSection,
		},
		{
			// The regression. No bd marker anywhere; "beads" appears only in a
			// comment and in a call to bd, both of which the old first-ten-lines
			// substring match treated as bd's signature.
			name:    "hand-composed hook that merely mentions beads",
			content: composedForeignHook,
			want:    hookNotOurs,
		},
		{
			name:    "unrelated hook",
			content: "#!/bin/sh\n. \"$(dirname -- \"$0\")/_/husky.sh\"\nnpm test\n",
			want:    hookNotOurs,
		},
		{
			// "beads" past the tenth line was invisible to the old scan, so the
			// old code was wrong in both directions on the same input class.
			name:    "mentions beads only far down the file",
			content: "#!/bin/sh\n#\n#\n#\n#\n#\n#\n#\n#\n#\n#\n# runs beads later\nexit 0\n",
			want:    hookNotOurs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeHook(t, t.TempDir(), "pre-commit", tt.content)
			if got := classifyResetHook(path); got != tt.want {
				t.Errorf("classifyResetHook() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyResetHook_MissingFileIsNotOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-commit")
	if got := classifyResetHook(path); got != hookNotOurs {
		t.Errorf("classifyResetHook(missing) = %v, want hookNotOurs", got)
	}
}

// Two functions decide, separately, whether a hook file holds anything of the
// user's: shouldPreserveHookContent when bd migrates hooks into .beads/hooks,
// and classifyResetHook when bd is about to delete them. They must not drift
// into disagreement, but the agreement they owe each other is one-way, because
// the cost of being wrong is not symmetric:
//
//   - Preservation copying too little loses a comment out of a file that still
//     exists on disk.
//   - Reset deleting too much destroys the user's hook with no way back —
//     performReset only renames <hook>.backup into place, and a backup exists
//     only for hooks bd itself displaced.
//
// So the invariant is: whatever preservation treats as the user's, reset must
// keep; and reset may additionally keep files preservation was willing to drop
// (a hook whose own content is only comments is the case that differs). What
// reset may never do is call a marker-carrying file none of bd's business —
// that would leave a hook bd wrote installed after a reset claimed to remove it.
//
// This runs over the repository's own tracked .githooks rather than fixtures,
// so the inputs are hooks a person actually composed. It asserts the invariant,
// not a hardcoded verdict per file, so editing those hooks cannot make it lie.
func TestClassifyResetHook_AgreesWithHookPreservation(t *testing.T) {
	for _, name := range managedHookNames {
		path := filepath.Join("..", "..", ".githooks", name)
		content, err := os.ReadFile(path) // #nosec G304 -- fixed repo-relative path
		if err != nil {
			t.Skipf("%s not present in this checkout: %v", path, err)
		}
		t.Run(name, func(t *testing.T) {
			_, wouldPreserve := shouldPreserveHookContent(string(content), false)
			got := classifyResetHook(path)

			if wouldPreserve && got == hookBdOwned {
				t.Errorf("classifyResetHook(%s) = hookBdOwned, but shouldPreserveHookContent keeps this file: "+
					"reset would delete content bd itself considers the user's, with no backup to restore", path)
			}
			if !wouldPreserve && got == hookNotOurs {
				t.Errorf("classifyResetHook(%s) = hookNotOurs, but shouldPreserveHookContent discards this file as "+
					"wholly bd-managed: a hook bd wrote would survive a reset unremoved and unreported", path)
			}
		})
	}
}

// The divergence between the two emptiness predicates is the fix, so pin it.
// A later cleanup that notices they answer "the same" question and collapses
// them into one has to fail here rather than quietly restore the deletion.
func TestResetEmptinessPredicateIsStricterThanPreservation(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		preservationSees bool // isOnlyShebangOrEmpty: comments are nothing
		resetSees        bool // isOnlyShebangOrBlank: comments are content
	}{
		{"empty", "", true, true},
		{"shebang only", "#!/bin/sh\n", true, true},
		{"shebang and blank lines", "#!/bin/sh\n\n\n", true, true},
		{"shebang and a comment", "#!/bin/sh\n# team hook, see docs/hooks.md\n", true, false},
		{"comment with no shebang", "# nothing here yet\n", true, false},
		{"commented-out logic", "#!/bin/sh\n# ./scripts/lint\n", true, false},
		{"real logic", "#!/bin/sh\n./scripts/lint\n", false, false},
		{"shebang below a blank line", "\n#!/bin/sh\n", true, true},
		{"second #! is a comment, not a shebang", "#!/bin/sh\n#!/bin/false\n", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOnlyShebangOrEmpty(tt.content); got != tt.preservationSees {
				t.Errorf("isOnlyShebangOrEmpty(%q) = %v, want %v", tt.content, got, tt.preservationSees)
			}
			if got := isOnlyShebangOrBlank(tt.content); got != tt.resetSees {
				t.Errorf("isOnlyShebangOrBlank(%q) = %v, want %v", tt.content, got, tt.resetSees)
			}
			if tt.resetSees && !tt.preservationSees {
				t.Errorf("%q: reset's predicate must never call empty what preservation calls content — "+
					"that is the direction that deletes", tt.content)
			}
		})
	}
}

func TestCollectResetItems_OnlyRemovesHooksBdOwns(t *testing.T) {
	gitCommonDir := t.TempDir()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	hooksDir := filepath.Join(gitCommonDir, "hooks")

	bdOwned := writeHook(t, hooksDir, "post-merge", "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) post-merge hook\nbd import\n")
	sectioned := writeHook(t, hooksDir, "pre-commit", "#!/bin/sh\nset -e\n./scripts/lint\n\n"+generateHookSection("pre-commit"))
	foreign := writeHook(t, hooksDir, "pre-push", composedForeignHook)
	commentOnly := writeHook(t, hooksDir, "post-checkout", "#!/bin/sh\n# post-checkout — ours; bd's block was added below.\n\n"+generateHookSection("post-checkout"))

	items, preserved := collectResetItems(gitCommonDir, beadsDir)

	inItems := map[string]bool{}
	for _, it := range items {
		inItems[it.Path] = true
	}
	inPreserved := map[string]bool{}
	for _, it := range preserved {
		inPreserved[it.Path] = true
	}

	if !inItems[bdOwned] {
		t.Errorf("bd-owned hook %s missing from removal list", bdOwned)
	}
	if inItems[sectioned] {
		t.Errorf("hook %s is the user's file with a bd section in it; removing it takes their content too", sectioned)
	}
	if !inPreserved[sectioned] {
		t.Errorf("hook %s should be reported as left in place, not silently ignored", sectioned)
	}
	if inItems[foreign] || inPreserved[foreign] {
		t.Errorf("hook %s carries no bd marker; it is neither ours to remove nor ours to mention", foreign)
	}
	if inItems[commentOnly] {
		t.Errorf("hook %s is the user's file — everything outside bd's section is a comment they wrote, "+
			"and reset has no backup to restore it from", commentOnly)
	}
	if !inPreserved[commentOnly] {
		t.Errorf("hook %s should be reported as left in place, not silently ignored", commentOnly)
	}

	// The .beads directory is always in the removal list — that is the point of
	// the command, and this pins that the hook change did not disturb it.
	if !inItems[beadsDir] {
		t.Errorf("beads dir %s missing from removal list", beadsDir)
	}
}
