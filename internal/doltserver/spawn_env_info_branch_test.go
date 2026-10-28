package doltserver

import (
	"os"
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

// TestServerSpawnEnvDisablesRemoteInfoBranch pins the info-branch guard on
// the spawned sql-server (GH#6484): the server runs CALL DOLT_PUSH/FETCH
// itself and Dolt resolves and caches the info-branch choice per process, so
// the child's environment must carry DOLT_REMOTE_INFO_BRANCH set to empty —
// Dolt's opt-out from publishing the visible info branch — whenever bd's
// own environment does not set it.
func TestServerSpawnEnvDisablesRemoteInfoBranch(t *testing.T) {
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(doltinfoenv.EnvVar) })

	entries := remoteInfoBranchEntries(ServerSpawnEnv())
	if len(entries) != 1 || entries[0] != doltinfoenv.EnvVar+"=" {
		t.Errorf("ServerSpawnEnv() %s entries = %q, want exactly one set to empty", doltinfoenv.EnvVar, entries)
	}
}

// TestServerSpawnEnvPreservesUserRemoteInfoBranch pins the opt-in: a value
// the user set in bd's environment — a branch name, or empty, Dolt's own
// opt-out form — must reach the spawned server unchanged, exactly once.
func TestServerSpawnEnvPreservesUserRemoteInfoBranch(t *testing.T) {
	for _, set := range []string{"dolt-info", ""} {
		t.Run("value "+set, func(t *testing.T) {
			t.Setenv(doltinfoenv.EnvVar, set)
			entries := remoteInfoBranchEntries(ServerSpawnEnv())
			want := doltinfoenv.EnvVar + "=" + set
			if len(entries) != 1 || entries[0] != want {
				t.Errorf("ServerSpawnEnv() %s entries = %q, want exactly one %q", doltinfoenv.EnvVar, entries, want)
			}
		})
	}
}
