package ownershiphandoffv2

import (
	"path/filepath"
	"strings"
)

// canonicalPath is the one path rule: every path this package records, and
// every path it later compares against a recorded one, goes through here first.
//
// A workspace can be named more than one way — a symlinked $HOME, an NFS
// automount, a symlinked .beads, macOS's /var pointing at /private/var — and
// two spellings of one directory must never be read as two directories. That
// mistake is not cosmetic here: the fence compares the journal's root against
// the workspace it was found in, so a mismatch turns a COMMITTED journal, the
// state that means "bd owns this scope, carry on", into a refusal that bricks
// every ordinary command in the workspace.
//
// Resolution is best-effort. A path that does not exist yet cannot be resolved
// and still has to compare equal to itself, so an unresolvable path falls back
// to Clean rather than to an error: the comparison degrades to a string match,
// which is what the code did everywhere before this rule existed.
func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// samePath reports whether two paths name the same location, resolving both
// sides first. Use it for every path equality test in this package.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return canonicalPath(a) == canonicalPath(b)
}

// pathUnder reports whether path is root itself or a descendant of it, with
// both sides resolved. A relocated data dir reached through a symlink is still
// inside the workspace that owns it.
func pathUnder(path, root string) bool {
	path, root = canonicalPath(path), canonicalPath(root)
	if root == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}
