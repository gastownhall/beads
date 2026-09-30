package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

// TestInitGitBootstrapProbeWidensPastLegitimateCeiling pins the git-bootstrap decision in
// `bd init` — `initArtifactGitCommand(cwd, "rev-parse", "--git-dir").Run() != nil` gates the
// fresh `git init` — against the *widening* half of the GIT_CEILING_DIRECTORIES membership
// decision documented on gitenv.IsRoutingKeyForOS.
//
// Every other ceiling assertion in the tree checks only that the key is scrubbed. Scrubbing a
// ceiling that was set legitimately widens the upward search rather than narrowing it, so a
// working directory that is not itself a repository now resolves the containing one: the probe
// succeeds and bd therefore does *not* create a fresh repository in cwd. That is the
// user-visible consequence — an operator who fenced off an ancestor repository previously got a
// fresh `git init` in cwd and now gets .beads/ inside the ancestor's working tree. It is
// deliberate, because an inherited ceiling must not take working-directory authority away from
// the selected project directory, but it is the half a future change to routingKeys would break
// silently.
func TestInitGitBootstrapProbeWidensPastLegitimateCeiling(t *testing.T) {
	ancestor := t.TempDir()
	initRepo := exec.Command("git", "init", "--quiet")
	initRepo.Dir = ancestor
	initRepo.Env = gitenv.ScrubRouting(os.Environ())
	if out, err := initRepo.CombinedOutput(); err != nil {
		t.Fatalf("initialize ancestor repository: %v\n%s", err, out)
	}

	// The selected project directory: inside the ancestor's working tree, not a repository
	// itself. This is the shape `bd init` faces at the bootstrap probe.
	project := filepath.Join(ancestor, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	// git resolves symlinks in ceiling entries before comparing, and a temp root can be
	// symlinked (/tmp -> /private/tmp), so fence with the real path.
	ceiling, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", ancestor, err)
	}

	// Control: identical environment except the ceiling is left in place, which is what the
	// probe did before this change. It must fail — otherwise the ceiling never bit and the
	// assertion below would pass for the wrong reason.
	fenced := exec.Command("git", "-C", project, "rev-parse", "--git-dir")
	fenced.Env = append(gitenv.ScrubRouting(os.Environ()), "GIT_CEILING_DIRECTORIES="+ceiling)
	if out, ferr := fenced.Output(); ferr == nil {
		t.Fatalf("ceiling %q did not fence off %q (git found %q); this case cannot prove the widening",
			ceiling, project, strings.TrimSpace(string(out)))
	}

	t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)

	if err := initArtifactGitCommand(project, "rev-parse", "--git-dir").Run(); err != nil {
		t.Fatalf("bootstrap probe honored the inherited ceiling (%v); `bd init` would create a "+
			"second repository inside %s instead of reusing it", err, ancestor)
	}
}
