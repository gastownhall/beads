package remotecache

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

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

// TestDoltCmdTearsDownGracefully pins the GH#7989 teardown contract for cache
// transfers: the dolt CLI child bd spawns must not be killed hard the moment
// its context expires. It gets a graceful cancel hook (SIGTERM to the
// immediate child on unix, so the child can reap the transport children it
// spawned) and a bounded WaitDelay, so a child — or a grandchild holding the
// inherited pipes — cannot block its reaping forever.
func TestDoltCmdTearsDownGracefully(t *testing.T) {
	cmd := doltCmd(context.Background(), t.TempDir(), "push", "origin", "main")
	if cmd.WaitDelay <= 0 || cmd.WaitDelay > time.Minute {
		t.Fatalf("doltCmd() WaitDelay = %s, want a bounded nonzero window (GH#7989)", cmd.WaitDelay)
	}
	if runtime.GOOS == "windows" {
		t.Skip("windows keeps the default kill cancellation; only the bounded pipe wait applies")
	}
	if cmd.Cancel == nil {
		t.Fatal("doltCmd() left Cancel nil; the dolt child would be killed hard instead of tearing down gracefully (GH#7989)")
	}
}
