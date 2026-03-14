package doctor

import "github.com/gastownhall/beads/internal/beads"

// resolveBeadsDir follows any redirect/symlink in the beads directory.
// Extracted from maintenance.go to be available in non-CGO builds.
func resolveBeadsDir(beadsDir string) string {
	return beads.FollowRedirect(beadsDir)
}
