package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltversion"
)

// TestPrepareDoltCLITransferCommandScrubsGitRoutingEnv proves the dolt child
// never inherits bd's git routing selection: under `bd -C`, retargetGitContext
// points GIT_DIR/GIT_WORK_TREE at the -C repository for bd's own git surfaces,
// and a dolt transfer that leaked them would die with "GIT_WORK_TREE not
// allowed without specifying GIT_DIR" the moment its git plumbing touched a
// git-protocol remote.
func TestPrepareDoltCLITransferCommandScrubsGitRoutingEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/caller/repo/.git")
	t.Setenv("GIT_WORK_TREE", "/caller/repo")
	t.Setenv("GIT_INDEX_FILE", "/caller/repo/.git/index")

	stubDoltVersionOnPath(t, doltversion.RequiredMin.String())
	cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "push", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	visible := map[string]string{}
	for _, entry := range cmd.Env {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			visible[key] = value
		}
	}
	if len(visible) != 0 {
		t.Errorf("routing keys leaked into the dolt child env: %v", visible)
	}
	if _, ok := lookupEnvEntry(cmd.Env, "PATH"); !ok {
		t.Errorf("PATH missing from the dolt child env; the scrub removed non-routing controls")
	}
}

func lookupEnvEntry(env []string, key string) (string, bool) {
	for _, entry := range env {
		if k, value, found := strings.Cut(entry, "="); found && k == key {
			return value, true
		}
	}
	return "", false
}
