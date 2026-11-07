package routing

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestMain drops a GIT_DIR/GIT_INDEX_FILE leaked in from the invoking shell or
// a git hook. Left in place, it routes the fixture `git init`/`git commit`
// writes in this suite into that repository (gastownhall/beads#7706).
func TestMain(m *testing.M) {
	if err := testutil.UnsetGitRepoLocationEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
