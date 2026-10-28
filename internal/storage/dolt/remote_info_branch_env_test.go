package dolt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltinfoenv"
)

// remoteInfoBranchEntries returns the DOLT_REMOTE_INFO_BRANCH entries in
// env, so a test can pin both presence and value.
func remoteInfoBranchEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, doltinfoenv.EnvVar+"=") || kv == doltinfoenv.EnvVar {
			out = append(out, kv)
		}
	}
	return out
}

// TestPrepareDoltCLITransferCommandEnvDisablesRemoteInfoBranch pins the
// info-branch guard on the CLI transfer route (GH#6484): every dolt
// push/pull/fetch child bd spawns must see DOLT_REMOTE_INFO_BRANCH set to
// empty, Dolt's opt-out from publishing the visible info branch, whenever
// bd's own environment does not set it.
func TestPrepareDoltCLITransferCommandEnvDisablesRemoteInfoBranch(t *testing.T) {
	withNoDoltOnPath(t)
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(doltinfoenv.EnvVar) })

	for _, args := range [][]string{{"push", "origin", "main"}, {"pull", "origin", "main"}, {"fetch", "origin"}} {
		cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, args...)
		if err != nil {
			t.Fatalf("prepareDoltCLITransferCommand(%v): %v", args, err)
		}
		entries := remoteInfoBranchEntries(cmd.Env)
		if len(entries) != 1 || entries[0] != doltinfoenv.EnvVar+"=" {
			t.Errorf("dolt %s env %s entries = %q, want exactly one set to empty", args[0], doltinfoenv.EnvVar, entries)
		}
		cancel()
	}
}

// TestBootstrapCloneCmdEnvDisablesRemoteInfoBranch pins the same guard on the
// bootstrap clone: it does not route through prepareDoltCLITransferCommand,
// so it must apply the env guard itself.
func TestBootstrapCloneCmdEnvDisablesRemoteInfoBranch(t *testing.T) {
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(doltinfoenv.EnvVar) })

	cmd := bootstrapCloneCmd(context.Background(), "git+https://example.com/repo.git", filepath.Join(t.TempDir(), "beads"), "")
	entries := remoteInfoBranchEntries(cmd.Env)
	if len(entries) != 1 || entries[0] != doltinfoenv.EnvVar+"=" {
		t.Errorf("dolt clone env %s entries = %q, want exactly one set to empty", doltinfoenv.EnvVar, entries)
	}
}
