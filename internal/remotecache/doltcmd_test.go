package remotecache

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/githooksenv"
)

// Cache push/clone/pull run git plumbing for a git+ remote: the spawned env
// needs git tracing scrubbed and templated hooks disabled (GH#4272).
func TestDoltCmdEnvIsGuarded(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_CURL_VERBOSE", "1")

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
}
