package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// startProxyInFrontOfTestServer runs a real bd db-proxy, rooted at a fresh
// directory, in front of the shared test Dolt server, and returns its root
// and data port. Each caller gets its own proxy, so a refused open cannot
// trip the circuit breaker another subtest's address relies on.
func startProxyInFrontOfTestServer(t *testing.T) (root string, port int) {
	t.Helper()
	root = t.TempDir()
	backend, err := server.NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: testServerPort})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	p := proxy.NewProxyServer(proxy.ProxyOpts{RootDir: root, Server: backend})
	go func() { done <- p.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("proxy did not exit within 10s of cancel")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pf, err := pidfile.Read(root, proxy.PIDFileName)
		if err == nil && pf != nil {
			return root, pf.Port
		}
		select {
		case err := <-done:
			t.Fatalf("proxy exited before it published its pidfile: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("proxy pidfile not present within 10s")
	return "", 0
}

// TestServerProxyRoot pins the server-mode open through a bd db-proxy's data
// port: with ServerProxyRoot (or BEADS_DOLT_SERVER_PROXY_ROOT) naming the
// proxy's root, every connection completes the BDPROXY1 exchange and the
// store opens, creating and migrating its database through the proxy.
// Without it the data port refuses the plain MySQL dial, and under another
// root's secret the open fails with proxy.ErrDataAuth.
func TestServerProxyRoot(t *testing.T) {
	skipIfNoServer(t)
	t.Setenv("BEADS_DOLT_SERVER_PROXY_ROOT", "")

	open := func(t *testing.T, port int, proxyRoot string) (*DoltStore, error) {
		t.Helper()
		// Creating and migrating a fresh database takes the open about 20s on
		// a quiet host (the same open made directly to the test server takes
		// as long), and three times that under a loaded one.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return New(ctx, &Config{
			Path:             t.TempDir(),
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerProxyRoot:  proxyRoot,
			Database:         fmt.Sprintf("test_proxy_root_%d", time.Now().UnixNano()),
			MaxOpenConns:     1,
			CreateIfMissing:  true,
			DisableAutoStart: true,
		})
	}

	t.Run("the proxy's root", func(t *testing.T) {
		root, port := startProxyInFrontOfTestServer(t)
		store, err := open(t, port, root)
		if err != nil {
			t.Fatalf("open through the proxy with its root: %v", err)
		}
		defer store.Close()
		if !store.CreatedDatabase() {
			t.Error("CreatedDatabase() = false for the open that created the database through the proxy")
		}
		var one int
		if err := store.db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("SELECT 1 through the proxy = %d, %v", one, err)
		}
	})

	t.Run("the proxy's root from the environment", func(t *testing.T) {
		root, port := startProxyInFrontOfTestServer(t)
		t.Setenv("BEADS_DOLT_SERVER_PROXY_ROOT", root)
		store, err := open(t, port, "")
		if err != nil {
			t.Fatalf("open through the proxy with BEADS_DOLT_SERVER_PROXY_ROOT: %v", err)
		}
		_ = store.Close()
	})

	t.Run("no proxy root", func(t *testing.T) {
		_, port := startProxyInFrontOfTestServer(t)
		store, err := open(t, port, "")
		if err == nil {
			_ = store.Close()
			t.Fatal("a plain MySQL open of a proxy data port succeeded")
		}
		t.Logf("plain MySQL open of the data port: %v", err)
	})

	t.Run("another root", func(t *testing.T) {
		_, port := startProxyInFrontOfTestServer(t)
		other := t.TempDir()
		if _, err := identity.WriteSecret(other); err != nil {
			t.Fatal(err)
		}
		store, err := open(t, port, other)
		if err == nil {
			_ = store.Close()
			t.Fatal("an open under another root's proxy secret succeeded")
		}
		if !errors.Is(err, proxy.ErrDataAuth) {
			t.Fatalf("open under another root's proxy secret = %v; want proxy.ErrDataAuth", err)
		}
		t.Logf("open under another root's secret: %v", err)
	})
}

// TestServerProxyRootRefusesSocket pins that a proxy root, which names a TCP
// data port, is refused beside a Unix socket rather than one of them being
// silently ignored.
func TestServerProxyRootRefusesSocket(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PROXY_ROOT", "")
	_, err := New(context.Background(), &Config{
		Path:             t.TempDir(),
		ServerHost:       "127.0.0.1",
		ServerPort:       testServerPortOr(3999),
		ServerSocket:     "/nonexistent/dolt.sock",
		ServerProxyRoot:  t.TempDir(),
		Database:         "test_proxy_root_socket",
		DisableAutoStart: true,
	})
	if err == nil || !strings.Contains(err.Error(), "a proxy listens on TCP only") {
		t.Fatalf("open with a proxy root and a socket = %v; want the TCP-only refusal", err)
	}
}

func testServerPortOr(port int) int {
	if testServerPort != 0 {
		return testServerPort
	}
	return port
}
