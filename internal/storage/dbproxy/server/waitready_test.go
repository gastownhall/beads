package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/stretchr/testify/require"
)

// fakeMySQLGreeting is a minimal stand-in for a MySQL handshake packet; only
// its non-emptiness matters to doltserver.DrainAndCloseProbe.
var fakeMySQLGreeting = []byte("\x0a5.7.9-fake\x00")

// newConfigForListener builds a servercfg.ServerConfig whose Host()/Port()
// point at ln. YAMLConfig is the simplest settable ServerConfig
// implementation in the servercfg package: DefaultServerConfig() returns one
// (unexported behind the interface), and its exported fields are plain
// pointers, so a struct literal is enough here without needing a config file
// on disk.
func newConfigForListener(t *testing.T, ln net.Listener) servercfg.ServerConfig {
	t.Helper()
	addr := ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	port := addr.Port
	return servercfg.YAMLConfig{
		ListenerConfig: servercfg.ListenerYAMLConfig{
			HostStr:    &host,
			PortNumber: &port,
		},
	}
}

// TestWaitReadyGreetedListener verifies waitReady declares the server ready
// once a probe dial both succeeds and observes a MySQL greeting.
func TestWaitReadyGreetedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = c.Write(fakeMySQLGreeting)
				time.Sleep(20 * time.Millisecond)
				_ = c.Close()
			}(conn)
		}
	}()

	s := &DoltServer{
		config:          newConfigForListener(t, ln),
		egCtx:           context.Background(),
		keepAlivePeriod: time.Second,
	}

	done := make(chan error, 1)
	go func() {
		done <- s.waitReady(context.Background())
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "waitReady should succeed against a listener that sends a greeting")
	case <-time.After(5 * time.Second):
		t.Fatal("waitReady did not return within 5s against a greeted listener")
	}
}

// TestWaitReadyMuteListenerNotReady verifies waitReady keeps polling (and
// does not declare readiness) when the listener accepts connections but
// never writes a MySQL greeting.
func TestWaitReadyMuteListenerNotReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Accept and hold the connection open without writing anything,
			// then close it once the test is done.
			go func(c net.Conn) {
				<-stop
				_ = c.Close()
			}(conn)
		}
	}()

	s := &DoltServer{
		config:          newConfigForListener(t, ln),
		egCtx:           context.Background(),
		keepAlivePeriod: time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = s.waitReady(ctx)
	elapsed := time.Since(start)

	require.Error(t, err, "waitReady must not declare readiness against a mute listener")
	require.Lessf(t, elapsed, 2*time.Second, "waitReady took %s; expected it to give up shortly after the 600ms context deadline, not run the full 30s internal deadline", elapsed)
}
