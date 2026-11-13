//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// TestInitProxiedServerEphemeralRootRefusesExplicitNever drives init.go's
// BEADS_EPHEMERAL_ROOT wiring through a real `bd init --proxied-server`: RunE
// reads the variable and hands it to validateEphemeralIdleTimeout. With the
// variable set, an explicit "never" idle timeout must abort before init writes
// anything. The refusal comes from flag validation, ahead of any proxy or Dolt
// work, so this test needs no Dolt binary.
func TestInitProxiedServerEphemeralRootRefusesExplicitNever(t *testing.T) {
	bd := buildEmbeddedBD(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	home := t.TempDir()
	// If the guard ever stops firing, init goes on to start a real proxy and
	// Dolt server; shut them down so that failure does not also leak them.
	t.Cleanup(func() {
		proxyRoot := filepath.Join(repoDir, ".beads", "dolt")
		if _, err := os.Stat(proxyRoot); err == nil {
			_ = proxy.Shutdown(proxyRoot)
		}
	})

	cmd := exec.Command(bd, "init", "--proxied-server", "--quiet", "--prefix", "eph",
		"--non-interactive", "--skip-hooks", "--skip-agents",
		"--proxied-server-idle-timeout=0")
	cmd.Dir = repoDir
	cmd.Env = append(bdProxiedEnv(home), "BEADS_EPHEMERAL_ROOT=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init accepted --proxied-server-idle-timeout=0 with BEADS_EPHEMERAL_ROOT=1:\n%s", out)
	}
	for _, want := range []string{"BEADS_EPHEMERAL_ROOT=1", "--proxied-server-idle-timeout"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("refusal does not name %q:\n%s", want, out)
		}
	}
	for _, dir := range []string{filepath.Join(repoDir, ".beads"), filepath.Join(home, ".beads")} {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("bd init created %s before refusing (stat: %v)", dir, statErr)
		}
	}
}

// TestProxiedServerInitEphemeralRootKeepsBuiltInIdleDefault is the other half
// of that wiring: with the idle-timeout flag omitted, BEADS_EPHEMERAL_ROOT=1
// must not refuse, and it must not substitute an idle window of its own. The
// variable only guards against an explicit "never". Whether a flagless init
// writes a sidecar at all, and with which idle timeout, is decided elsewhere,
// so this compares against the same init without the variable rather than
// pinning either outcome.
func TestProxiedServerInitEphemeralRootKeepsBuiltInIdleDefault(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	plain := bdProxiedInit(t, bd, "plain")
	ephemeral := bdProxiedInitWithEnv(t, bd, "eph", []string{"BEADS_EPHEMERAL_ROOT=1"})

	load := func(p proxiedProject) *configfile.ProxiedServerClientInfo {
		t.Helper()
		info, err := configfile.LoadProxiedServerClientInfo(p.beadsDir)
		if err != nil {
			t.Fatalf("LoadProxiedServerClientInfo(%s): %v", p.beadsDir, err)
		}
		return info
	}
	want, got := load(plain), load(ephemeral)
	if (want == nil) != (got == nil) {
		t.Fatalf("BEADS_EPHEMERAL_ROOT=1 changed whether init writes %s: without it %+v, with it %+v",
			configfile.ProxiedServerClientInfoFileName, want, got)
	}
	if want != nil && got.IdleTimeout != want.IdleTimeout {
		t.Fatalf("persisted idle timeout with BEADS_EPHEMERAL_ROOT=1 = %s, want %s (the same as without it)",
			got.IdleTimeout, want.IdleTimeout)
	}
}
