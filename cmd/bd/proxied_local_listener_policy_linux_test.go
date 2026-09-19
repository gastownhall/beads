//go:build cgo && linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func TestManagedLocalProxiedListenerPolicy(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)

	t.Run("non-loopback custom config refused at init", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		configPath := writeListenerConfig(
			t,
			filepath.Join(t.TempDir(), "non-loopback.yaml"),
			"0.0.0.0",
			false,
			false,
		)

		stdout, stderr, err := bdProxiedRunBuffersWithEnv(
			t,
			bd,
			dir,
			[]string{"BEADS_PROXIED_SERVER_CONFIG=" + configPath},
			"init",
			"--proxied-server",
			"--quiet",
			"--prefix", "mlpolicybad",
			"--non-interactive",
			"--skip-agents",
			"--skip-hooks",
		)
		if err == nil {
			t.Fatalf("managed init unexpectedly accepted 0.0.0.0:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		assertManagedListenerPolicyOutput(t, stdout+stderr)
	})

	t.Run("non-loopback config under flagged root path refused at init", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		rootDir := t.TempDir()
		writeListenerConfig(
			t,
			filepath.Join(rootDir, proxiedServerConfigName),
			"0.0.0.0",
			false,
			false,
		)

		stdout, stderr, err := bdProxiedRunBuffersWithEnv(
			t,
			bd,
			dir,
			nil,
			"init",
			"--proxied-server",
			"--proxied-server-root-path", rootDir,
			"--quiet",
			"--prefix", "mlpolicyroot",
			"--non-interactive",
			"--skip-agents",
			"--skip-hooks",
		)
		if err == nil {
			t.Fatalf("managed init unexpectedly accepted 0.0.0.0 under --proxied-server-root-path:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		assertManagedListenerPolicyOutput(t, stdout+stderr)
	})

	t.Run("loopback custom config completes lifecycle then hand edit is refused", func(t *testing.T) {
		// PickFreePort here recreates the bind-close-launch race this PR
		// removes from the proxy itself: the port is released before the
		// managed dolt sql-server binds it. That is inherent to handing a
		// pre-written custom config a concrete backend port, so the small
		// EADDRINUSE flake window is accepted and documented rather than
		// retried around.
		backendPort, err := proxy.PickFreePort()
		if err != nil {
			t.Fatalf("pick custom backend port: %v", err)
		}
		configPath := writeListenerConfigWithPort(
			t,
			filepath.Join(t.TempDir(), "loopback.yaml"),
			"127.0.0.1",
			backendPort,
		)

		p := bdManagedLocalInit(
			t,
			bd,
			"mlpolicyok",
			5*time.Minute,
			"--proxied-server-config-path", configPath,
		)
		held := openHeldManagedProxiedConn(t, bd, p)
		issue := bdProxiedCreate(t, bd, p.dir, "loopback custom policy issue")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var title string
		err = held.Conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issue.ID).Scan(&title)
		cancel()
		if err != nil {
			t.Fatalf("SQL round-trip through loopback custom config: %v", err)
		}
		if title != issue.Title {
			t.Fatalf("SQL round-trip title: got %q, want %q", title, issue.Title)
		}
		held.Release()

		if err := proxy.Shutdown(p.proxyRoot); err != nil {
			t.Fatalf("stop managed proxy before runtime policy check: %v", err)
		}
		writeListenerConfigWithPort(t, configPath, "0.0.0.0", backendPort)

		out, err := bdProxiedRun(t, bd, p.dir, "list", "--json")
		if err == nil {
			t.Fatalf("runtime resolution unexpectedly accepted hand-edited 0.0.0.0 config:\n%s", out)
		}
		assertManagedListenerPolicyOutput(t, string(out))
	})
}

func writeListenerConfigWithPort(t *testing.T, path, host string, port int) string {
	t.Helper()
	body := []byte(fmt.Sprintf("listener:\n  host: %q\n  port: %d\n", host, port))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write listener config %s: %v", path, err)
	}
	return path
}

func assertManagedListenerPolicyOutput(t *testing.T, output string) {
	t.Helper()
	for _, want := range []string{
		"numeric loopback IP literal",
		"127.0.0.1",
		"--proxied-server-external-host",
		"--proxied-server-external-port",
		"--proxied-server-external-socket-path",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("listener-policy output missing %q:\n%s", want, output)
		}
	}
}
