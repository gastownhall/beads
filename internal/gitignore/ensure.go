package gitignore

import (
	"os"
	"path/filepath"
	"strings"
)

// EnsurePatternIgnored appends pattern to dir/.gitignore as a standalone line
// if it is not already present (matched exactly, after trimming whitespace,
// against each existing line — the same check
// cmd/bd/doctor.EnsureGitignoreForBeadsDir uses for its own required
// patterns). It creates the file (0600) when it does not exist yet.
//
// This is the minimal sibling of doctor.EnsureGitignoreForBeadsDir, not a
// move of it: that function also owns the full canonical .beads/.gitignore
// template, repairs permissions on an existing file, and covers every
// pattern bd's own on-disk state needs — and the cmd/bd/doctor package as a
// whole pulls in dolt and git-process dependencies through its other files.
// A caller like backend/http, which promises an embedder a minimal dependency
// footprint, cannot import cmd/bd/doctor just for this one line without
// breaking that promise. EnsurePatternIgnored exists so such a caller can
// still make the one guarantee it actually needs — this pattern is never
// offered to `git add` — on its own. The two are complementary and
// idempotent together: a workspace that goes through both ends up with the
// same file either way.
func EnsurePatternIgnored(dir, pattern string) error {
	path := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(path) // #nosec G304 -- caller supplies the active beads dir
	if os.IsNotExist(err) {
		return os.WriteFile(path, AppendLines(nil, []string{pattern}), 0o600)
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	return os.WriteFile(path, AppendLines(content, []string{pattern}), 0o600)
}
