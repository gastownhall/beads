package remotecache

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltinfoenv"
)

// TestDoltCmdEnvDisablesRemoteInfoBranch pins the info-branch guard on the
// cache transfers (GH#6484): doltCmd runs `dolt push origin main` against a
// git-protocol remote, so its environment must carry DOLT_REMOTE_INFO_BRANCH
// set to empty — Dolt's opt-out from publishing the visible info branch —
// whenever bd's own environment does not set it.
func TestDoltCmdEnvDisablesRemoteInfoBranch(t *testing.T) {
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(doltinfoenv.EnvVar) })

	cmd := doltCmd(t.Context(), t.TempDir(), "push", "origin", "main")
	if cmd.Env == nil {
		t.Fatal("doltCmd() left cmd.Env nil")
	}
	var entries []string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, doltinfoenv.EnvVar+"=") || kv == doltinfoenv.EnvVar {
			entries = append(entries, kv)
		}
	}
	if len(entries) != 1 || entries[0] != doltinfoenv.EnvVar+"=" {
		t.Errorf("doltCmd() %s entries = %q, want exactly one set to empty", doltinfoenv.EnvVar, entries)
	}
}
