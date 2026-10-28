package remotecache

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltinfoenv"
	"github.com/steveyegge/beads/internal/execenv"
	"github.com/steveyegge/beads/internal/githooksenv"
)

// Cache push/clone/pull run git plumbing for a git+ remote: the spawned env
// needs git tracing scrubbed, templated hooks disabled (GH#4272) and, with no
// DOLT_REMOTE_INFO_BRANCH of the user's, the info branch disabled (GH#6484).
func TestDoltCmdEnvIsGuarded(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_CURL_VERBOSE", "1")
	t.Setenv(doltinfoenv.EnvVar, "") // restored after the test
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatal(err)
	}

	cmd := doltCmd(context.Background(), t.TempDir(), "push", "origin", "main")
	if cmd.Env == nil {
		t.Fatal("doltCmd() left cmd.Env nil; the transfer would inherit stderr-directed git tracing")
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GIT_TRACE=") || strings.HasPrefix(kv, "GIT_CURL_VERBOSE=") {
			t.Errorf("doltCmd() kept %q; stderr-directed git tracing must be scrubbed", kv)
		}
	}
	if got := githooksenv.Extract(cmd.Env); !strings.Contains(got, githooksenv.NoHooksParam) {
		t.Errorf("doltCmd() effective %s = %q, want the no-hooks override (GH#4272)", githooksenv.ParametersEnv, got)
	}
	if got := doltInfoBranchEntries(cmd.Env); !slices.Equal(got, []string{doltinfoenv.EnvVar + "="}) {
		t.Errorf("doltCmd() %s entries = %q, want exactly one, empty (GH#6484)", doltinfoenv.EnvVar, got)
	}
}

// doltInfoBranchEntries returns env's DOLT_REMOTE_INFO_BRANCH entries.
func doltInfoBranchEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if execenv.KeyEqual(execenv.EntryKey(kv), doltinfoenv.EnvVar) {
			out = append(out, kv)
		}
	}
	return out
}
