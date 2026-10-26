package fix

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
)

// ClassicArtifacts removes what the Classic Artifacts scan reports as safe to
// delete under path, and nothing else: both halves walk the same .beads
// directories (WalkBeadsDirs) and act on the same plan (PlanArtifacts). That
// is an orphaned SQLite WAL or SHM in a .beads positively on Dolt, and the
// runtime leftovers of a redirect-only stub that holds no database state.
// Every removal goes through RemoveDisposable, which refuses database state.
// It reports each removal, each finding it leaves for review (what a stub
// keeps beside its runtime leftovers included) and a summary on out.
//
// It never migrates a .beads directory's metadata, and leaves a .beads
// directory in a format this build cannot open, or one whose ownership it
// cannot establish, untouched.
func ClassicArtifacts(path string, out io.Writer) error {
	var removed, skipped, errCount int

	err := WalkBeadsDirs(path, func(beadsDir string) {
		findings, _ := PlanArtifacts(beadsDir)
		for _, f := range findings {
			if !f.SafeDelete {
				fmt.Fprintf(out, "  Skip (needs review): %s: %s\n", f.Path, f.Description)
				skipped++
				continue
			}
			for _, target := range f.Remove {
				err := RemoveDisposable(target)
				var refused *RefusedError
				switch {
				case errors.As(err, &refused):
					fmt.Fprintf(out, "  Skip (needs review): %s: %s\n", target, refused.Reason)
					skipped++
				case err != nil:
					fmt.Fprintf(out, "  Error removing %s: %v\n", target, err)
					errCount++
				default:
					fmt.Fprintf(out, "  Removed: %s (%s)\n", target, removedLabel(f.Type))
					removed++
				}
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to walk directory tree: %w", err)
	}

	// Report summary
	fmt.Fprintf(out, "  Artifact cleanup: %d removed, %d skipped, %d errors\n", removed, skipped, errCount)

	if skipped > 0 {
		fmt.Fprintln(out, "  Skipped items need manual review: bd doctor never removes a database, anything in a .beads it cannot classify, or what a redirect-only .beads holds beyond bd's runtime leftovers")
	}

	if errCount > 0 {
		return fmt.Errorf("%d artifact(s) could not be removed", errCount)
	}

	return nil
}

func removedLabel(findingType string) string {
	if findingType == ArtifactCruft {
		return "cruft in redirect-only dir"
	}
	return "SQLite WAL/SHM without its database"
}

// isRedirectExpectedLocation returns true if this .beads directory should
// contain only a redirect file (a worktree or orchestrator subdirectory). It
// decides from the path alone; PlanArtifacts also requires the directory to
// hold no database state before it treats one as a stub. The polecats, crew
// and refinery patterns are retained for backwards compatibility with
// existing orchestrator installations.
func isRedirectExpectedLocation(beadsDir string) bool {
	parent := filepath.Dir(beadsDir)
	grandparent := filepath.Dir(parent)
	grandparentName := filepath.Base(grandparent)
	parentName := filepath.Base(parent)

	// Pattern: */polecats/*/.beads/ (orchestrator worker — backwards compat)
	if grandparentName == "polecats" {
		return true
	}
	// Pattern: */crew/*/.beads/ (orchestrator assistant — backwards compat)
	if grandparentName == "crew" {
		return true
	}
	// Pattern: */refinery/rig/.beads/ (orchestrator processor — backwards compat)
	if parentName == "rig" && grandparentName == "refinery" {
		return true
	}
	// Pattern: .git/beads-worktrees/*/.beads/
	if grandparentName == "beads-worktrees" {
		return true
	}
	// Rig-root .beads/ with a mayor/rig/.beads/ canonical location
	canonicalDir := filepath.Join(parent, "mayor", "rig", ".beads")
	if _, err := os.Stat(canonicalDir); err == nil {
		// When mayor/rig/.beads redirects here, this is the canonical
		// directory every checkout routes through, not a stub, whether or
		// not it holds a local store (a server-mode rig root does not).
		if redirectResolvesTo(canonicalDir, beadsDir) {
			return false
		}
		// Also check it has polecats/ or mayor/ siblings
		for _, sibling := range []string{"mayor", "polecats"} {
			if info, err := os.Stat(filepath.Join(parent, sibling)); err == nil && info.IsDir() {
				return true
			}
		}
	}

	return false
}

// redirectResolvesTo reports whether the redirect file in fromBeadsDir names
// target (redirectTarget), comparing the cleaned or symlink-resolved path. It
// does not apply FollowRedirect's rewrite of a detached /refs/commits/<sha>
// checkout to the branch worktree at that commit, and does not require the
// target to hold a database, so a target bd would currently refuse is still
// recognized as the one redirected to.
func redirectResolvesTo(fromBeadsDir, target string) bool {
	dest := redirectTarget(fromBeadsDir)
	if dest == "" {
		return false
	}
	if filepath.Clean(dest) == filepath.Clean(target) {
		return true
	}
	a, errA := filepath.EvalSymlinks(dest)
	b, errB := filepath.EvalSymlinks(target)
	return errA == nil && errB == nil && a == b
}

// redirectFollowed reports whether bd opens the target of beadsDir's
// redirect instead of beadsDir: beads.FollowRedirect honors a redirect only
// when it names a directory that discovery treats as a workspace
// (beads.HasProjectFiles). Like redirectResolvesTo, it does not apply
// FollowRedirect's rewrite of a detached /refs/commits/<sha> checkout.
func redirectFollowed(beadsDir string) bool {
	target := redirectTarget(beadsDir)
	return target != "" && beads.HasProjectFiles(target)
}

// redirectTarget is the path the redirect file in fromBeadsDir names, parsed
// as beads.FollowRedirect does: the first line that is neither blank nor a #
// comment, relative to fromBeadsDir's parent. It is "" when there is no
// readable redirect, or it names nothing.
func redirectTarget(fromBeadsDir string) string {
	data, err := os.ReadFile(filepath.Join(fromBeadsDir, "redirect")) // #nosec G304 -- a .beads redirect file doctor is classifying
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			if !filepath.IsAbs(line) {
				line = filepath.Join(filepath.Dir(fromBeadsDir), line)
			}
			return line
		}
	}
	return ""
}
