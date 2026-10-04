package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeChunks feeds s to a new watcher for port in the given chunk sizes
// (the last size repeats) and returns the watcher and what reached dst.
func writeChunks(t *testing.T, port int, s string, sizes ...int) (*startupWatch, string) {
	t.Helper()
	var dst bytes.Buffer
	w := newStartupWatch(nil, port)
	w.dst = &dst
	b := []byte(s)
	for i := 0; len(b) > 0; i++ {
		n := sizes[len(sizes)-1]
		if i < len(sizes) {
			n = sizes[i]
		}
		if n > len(b) {
			n = len(b)
		}
		got, err := w.Write(b[:n])
		require.NoError(t, err)
		require.Equal(t, n, got)
		b = b[n:]
	}
	return w, dst.String()
}

func TestStartupWatch(t *testing.T) {
	ready := `time="2026-10-04T05:45:58Z" level=info msg="Server ready. Accepting connections."` + "\n"
	pad := strings.Repeat("x", 5000) + "\n"
	for _, tc := range []struct {
		name      string
		port      int
		out       string
		sizes     []int
		ready     bool
		portInUse bool
	}{
		{name: "ready line in one write", port: 3306, out: ready, sizes: []int{1 << 20}, ready: true},
		{name: "ready line split across writes", port: 3306, out: ready, sizes: []int{40, 30, 1 << 20}, ready: true},
		{name: "ready line one byte per write", port: 3306, out: ready, sizes: []int{1}, ready: true},
		{name: "ready line with CRLF", port: 3306, out: strings.ReplaceAll(ready, "\n", "\r\n"), sizes: []int{7}, ready: true},
		{name: "ready line after more than the tail of output", port: 3306, out: pad + ready, sizes: []int{1000}, ready: true},
		{name: "MCP ready line is not the SQL ready line", port: 3306, out: "Dolt MCP server ready. Accepting connections.\n", sizes: []int{1 << 20}},
		{name: "dolt pre-check", port: 8000, out: "Port 8000 already in use.\n", sizes: []int{5}, portInUse: true},
		{name: "dolt pre-check for another port", port: 80, out: "Port 8000 already in use.\n", sizes: []int{1 << 20}},
		{name: "go-mysql-server pre-check", port: 41234, out: "Port 127.0.0.1:41234 already in use.\n", sizes: []int{3}, portInUse: true},
		{name: "kernel bind failure", port: 41234, out: "listen tcp 127.0.0.1:41234: bind: address already in use\n", sizes: []int{1}, portInUse: true},
		{name: "windows bind failure", port: 41234, out: "listen tcp 127.0.0.1:41234: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.\r\n", sizes: []int{9}, portInUse: true},
		{name: "another listener's bind failure", port: 41234, out: "listen tcp :9091: bind: address already in use\n", sizes: []int{1 << 20}},
		{name: "unix socket warning", port: 41234, out: "unix socket set up failed: bind address at given unix socket path is already in use\n", sizes: []int{1 << 20}},
		{name: "port-in-use after the tail of output", port: 41234, out: pad + "Port 41234 already in use.\n", sizes: []int{999}, portInUse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, got := writeChunks(t, tc.port, tc.out, tc.sizes...)
			assert.Equal(t, tc.out, got, "every byte must reach the log unchanged")
			assert.Equal(t, tc.ready, w.isReady())
			assert.Equal(t, tc.portInUse, w.sawPortInUse())
		})
	}
}

func TestStartupWatchNilLog(t *testing.T) {
	w := newStartupWatch(nil, 3306)
	n, err := w.Write([]byte("Server ready. Accepting connections.\n"))
	require.NoError(t, err)
	assert.Equal(t, 37, n)
	assert.True(t, w.isReady())
}

// newFakeDoltServer builds a DoltServer whose dolt is this test binary in
// fake mode (see fakeDolt in testmain_test.go) and whose config names port at
// log_level info.
func newFakeDoltServer(t *testing.T, mode string, port int) (*DoltServer, string) {
	t.Helper()
	t.Setenv("BEADS_TEST_FAKE_DOLT", mode)
	rootDir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf("log_level: info\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)), 0o600))
	s, err := NewDoltServer(os.Args[0], rootDir, cfg, filepath.Join(t.TempDir(), "server.log"), 0, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s, rootDir
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestDoltServer_Start_GivesUpAfterMaxPorts pins the bound on recovery: a
// dolt that reports its port taken on every launch ends Start after
// maxStartPortAttempts ports with ErrPortInUse naming the last one, and no
// runtime config is left behind.
func TestDoltServer_Start_GivesUpAfterMaxPorts(t *testing.T) {
	s, rootDir := newFakeDoltServer(t, "inuse", freeTestPort(t))
	asked := 0
	operatorPort := s.config.Port()
	s.SetPortConflictPolicy(func(_ string, inUsePort int) error {
		asked++
		assert.Equal(t, operatorPort, inUsePort, "the policy is asked about the operator config's port")
		return nil
	})

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPortInUse), "got %v", err)
	assert.Contains(t, err.Error(), fmt.Sprintf("gave up after %d ports", maxStartPortAttempts))
	assert.Equal(t, 1, asked, "the policy judges the operator config's port, so it is asked once")
	_, serr := os.Stat(filepath.Join(rootDir, RuntimeConfigFileName))
	assert.True(t, os.IsNotExist(serr), "a failed Start must not leave a runtime config")
}

// TestDoltServer_Start_MissingReadyLineIsDiagnosed pins SF4: a dolt that
// answers on its port but never logs the ready line is not accepted, and the
// timeout error names the missing line and the log_level escape hatch.
func TestDoltServer_Start_MissingReadyLineIsDiagnosed(t *testing.T) {
	old := startReadyTimeout
	// The fake is this test binary, which takes a few seconds to start.
	startReadyTimeout = 30 * time.Second
	t.Cleanup(func() { startReadyTimeout = old })

	s, _ := newFakeDoltServer(t, "silent", freeTestPort(t))
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never logged \"Server ready. Accepting connections.\"")
	assert.Contains(t, err.Error(), "although something answered")
	assert.Contains(t, err.Error(), "log_level to warning")
}

// TestDoltServer_Start_FakeReadyLine is the positive control for the fake.
func TestDoltServer_Start_FakeReadyLine(t *testing.T) {
	s, _ := newFakeDoltServer(t, "ready", freeTestPort(t))
	require.NoError(t, s.Start(context.Background()))
	assert.True(t, s.Running(context.Background()))
}

// TestUseRuntimePort_CopiesRawText pins that the runtime config carries the
// operator config's environment placeholders and "$$" escapes unexpanded:
// dolt interpolates the runtime file when it reads it, so expanding them here
// would expand them twice and put their values on disk. The server's own
// view (Dial, DSN) still uses the interpolated values.
func TestUseRuntimePort_CopiesRawText(t *testing.T) {
	t.Setenv("BD_TEST_HOST", "127.0.0.1")
	rootDir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("log_level: info\ndata_dir: \"a$$b\"\nlistener:\n  host: ${BD_TEST_HOST}\n  port: 40001\n"), 0o600))
	s, err := NewDoltServer(os.Args[0], rootDir, cfg, "", 0, "")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", s.config.Host())

	require.NoError(t, s.useRuntimePort(40001))

	body, err := os.ReadFile(filepath.Join(rootDir, RuntimeConfigFileName))
	require.NoError(t, err)
	assert.Contains(t, string(body), "${BD_TEST_HOST}", "placeholder must be copied, not expanded")
	assert.Contains(t, string(body), "a$$b", "a $$ escape must be copied, not unescaped")
	assert.Equal(t, "127.0.0.1", s.config.Host(), "the server dials the interpolated host")
	assert.NotEqual(t, 40001, s.config.Port())
	assert.Equal(t, filepath.Join(rootDir, RuntimeConfigFileName), s.launchConfigPath)
	info, err := os.Stat(filepath.Join(rootDir, RuntimeConfigFileName))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}
