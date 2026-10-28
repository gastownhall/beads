package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// decoySnapshot is the state of a decoy repository that a leaked git location
// would let a fixture rewrite: its history, its index and its config.
type decoySnapshot struct {
	head, count   string
	index, config []byte
}

// decoyGit runs git in dir with the location keys scrubbed, so it always acts
// on the decoy itself whatever the test process has in its environment.
func decoyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{}, noAutoMaintenance...), args...)...)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newDecoyRepo builds an outer repository holding one real commit, the shape
// of the checkout a git hook's children would inherit GIT_DIR from.
func newDecoyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	decoyGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoyGit(t, dir, "add", ".")
	decoyGit(t, dir, "-c", "user.name=Me", "-c", "user.email=me@x", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=", "commit", "-qm", "real work")
	return dir
}

// snapshotDecoy reads the decoy's state without refreshing its index.
func snapshotDecoy(t *testing.T, dir string) decoySnapshot {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	return decoySnapshot{
		head:   decoyGit(t, dir, "rev-parse", "HEAD"),
		count:  decoyGit(t, dir, "rev-list", "--count", "HEAD"),
		index:  index,
		config: config,
	}
}

// assertDecoyUnchanged fails the test when a fixture wrote into the decoy.
func assertDecoyUnchanged(t *testing.T, dir string, before decoySnapshot) {
	t.Helper()
	after := snapshotDecoy(t, dir)
	if after.head != before.head || after.count != before.count {
		t.Errorf("decoy history moved from %s (%s commits) to %s (%s commits):\n%s",
			before.head, before.count, after.head, after.count,
			decoyGit(t, dir, "log", "--format=%h %an %s", "-3"))
	}
	if !bytes.Equal(after.index, before.index) {
		t.Errorf("decoy index file was rewritten")
	}
	if !bytes.Equal(after.config, before.config) {
		t.Errorf("decoy .git/config was rewritten:\n%s", after.config)
	}
}
