package server_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stopTimeout = 15 * time.Second

const (
	// backendExitTimeout is how long a fixture waits for the dolt sql-server
	// to leave the process table after Stop returned. It is generous on
	// purpose: this only decides between "took a while" and "still there",
	// and only the second one is a defect.
	backendExitTimeout = 30 * time.Second
	backendExitPoll    = 50 * time.Millisecond
)

func requireDolt(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func writeConfig(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf(`log_level: debug
listener:
  host: 127.0.0.1
  port: %d
`, port)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func newDoltServer(t *testing.T) (*server.DoltServer, string) {
	t.Helper()
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	port := freePort(t)
	cfg := writeConfig(t, port)
	log := filepath.Join(t.TempDir(), "server.log")
	s, err := server.NewDoltServer(bin, rootDir, cfg, log, 0, "")
	require.NoError(t, err)
	// Close the log file handle before t.TempDir's RemoveAll runs.
	// On Windows, an open handle prevents directory removal; Stop is
	// idempotent so this is safe even when the test calls Stop itself.
	verifiedStopCleanup(t, s, rootDir)
	return s, rootDir
}

// verifiedStopCleanup is the stop the standard fixture registers. It
// stops the server and then asserts the `dolt sql-server` it recorded is
// actually gone.
//
// This cleanup used to be `_ = s.Stop(context.Background())`. Discarding the
// error made a failed stop indistinguishable from a clean one, and the
// t.TempDir() holding the server's working directory is removed AFTER this
// cleanup runs (t.TempDir registers its own cleanup first, so it runs last).
// A server that survives its Stop therefore ends the test as a live daemon
// serving a deleted directory. The suite-scoped post-run sweep catches that
// leak at package granularity; verifying here also names the responsible test
// before its cleanup deletes the evidence (wy-j2zc8q).
//
// The check runs on the clean path too, not only when Stop reports an error:
// "Stop returned nil" and "the server exited" are two different claims, and
// this is the fixture whose job is to hold the second one. It costs one
// identity probe against a record that a clean Stop has already removed.
func verifiedStopCleanup(t *testing.T, s *server.DoltServer, rootDir string) {
	t.Helper()
	t.Cleanup(func() {
		// Read the backend's record BEFORE stopping: a clean Stop removes
		// the pid file, so afterwards there is nothing left to verify
		// against.
		record := backendServerRecord(t, rootDir)
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Logf("DoltServer.Stop(%s): %v", rootDir, err)
		}
		requireBackendExited(t, record)
	})
}

// backendServerRecord returns the pid file this package's DoltServer wrote
// for its `dolt sql-server` child. nil means there is nothing to verify: no
// server was started, or it shut down cleanly and removed its record.
//
// The whole record is returned, not just the PID: Birth is the process-birth
// token that makes the PID safe to act on at all.
func backendServerRecord(t *testing.T, rootDir string) *pidfile.PidFile {
	t.Helper()
	pf, err := pidfile.Read(rootDir, server.PIDFileName)
	if err != nil {
		t.Logf("read %s in %s: %v", server.PIDFileName, rootDir, err)
		return nil
	}
	if pf == nil || pf.Pid <= 0 {
		return nil
	}
	return pf
}

// requireBackendExited fails the test if the recorded dolt sql-server is
// still running after Stop returned, then force-kills it so the leak does not
// outlive the run.
//
// Every step is gated on procid birth identity rather than the bare PID. Stop
// reaps the child, so its PID is reusable the instant it exits; signalling an
// unverified PID here could hit an unrelated process. A record with no birth
// token, or one whose token no longer matches, is left alone: not the server
// we recorded, nothing to report.
func requireBackendExited(t *testing.T, pf *pidfile.PidFile) {
	t.Helper()
	if pf == nil || pf.Pid <= 0 {
		return
	}
	if pf.Birth == "" {
		// A record from before birth tokens were written. There is no safe
		// way to ask about that PID, so the check is skipped — say so, rather
		// than looking like a silent pass.
		t.Logf("backend record for pid %d carries no birth token; skipping the survived-Stop check", pf.Pid)
		return
	}
	token := procid.Token(pf.Birth)

	deadline := time.Now().Add(backendExitTimeout)
	for {
		same, err := procid.Verify(pf.Pid, token)
		if err != nil {
			t.Logf("procid.Verify(%d): %v", pf.Pid, err)
			return
		}
		if !same {
			// Either the process is gone, or the PID now belongs to
			// something else. Either way our server is not running.
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(backendExitPoll)
	}

	handle, err := procid.Open(pf.Pid, token)
	if err != nil {
		if backendStillRunning(t, pf.Pid, token) {
			t.Errorf("dolt sql-server pid %d survived Stop by more than %s and could not be opened to kill: %v",
				pf.Pid, backendExitTimeout, err)
		}
		return
	}
	killErr := handle.Kill()
	_ = handle.Close()
	if killErr != nil {
		if backendStillRunning(t, pf.Pid, token) {
			t.Errorf("dolt sql-server pid %d survived Stop by more than %s and could not be killed: %v",
				pf.Pid, backendExitTimeout, killErr)
		}
		return
	}
	t.Errorf("dolt sql-server pid %d survived Stop by more than %s (force-killed)", pf.Pid, backendExitTimeout)
}

// backendStillRunning re-checks birth identity after procid.Open or
// Handle.Kill failed, and reports whether the recorded server is verifiably
// still there.
//
// Both calls verify the token themselves and return a plain "does not match
// token" error when it no longer does — which is exactly what a process that
// exited between the wait loop's last poll and the kill attempt produces.
// Without this second look, a server that shut down a few milliseconds late
// would be reported as one that survived Stop entirely. A verify that itself
// errors reports "not running": the run is over, and an inconclusive probe is
// not evidence of a leak.
func backendStillRunning(t *testing.T, pid int, token procid.Token) bool {
	t.Helper()
	same, err := procid.Verify(pid, token)
	if err != nil {
		t.Logf("procid.Verify(%d) after a failed kill attempt: %v", pid, err)
		return false
	}
	return same
}

func stopWithTimeout(t *testing.T, s *server.DoltServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	require.NoError(t, s.Stop(ctx))
}

func TestNewDoltServer_Validation(t *testing.T) {
	cfgDir := t.TempDir()
	goodCfg := filepath.Join(cfgDir, "config.yaml")
	require.NoError(t, os.WriteFile(goodCfg, []byte("log_level: debug\n"), 0o600))
	badYAML := filepath.Join(cfgDir, "bad.yaml")
	// Unclosed flow sequence — guaranteed YAML parse error.
	require.NoError(t, os.WriteFile(badYAML, []byte("foo: [bar\n"), 0o600))
	missingCfg := filepath.Join(cfgDir, "does-not-exist.yaml")

	cases := []struct {
		name string
		bin  string
		root string
		cfg  string
		want string
	}{
		{"empty bin", "", t.TempDir(), goodCfg, "doltBinExec is required"},
		{"empty root", "dolt", "", goodCfg, "rootDir is required"},
		{"empty cfg", "dolt", t.TempDir(), "", "configPath is required"},
		{"missing cfg", "dolt", t.TempDir(), missingCfg, "parse config"},
		{"bad yaml", "dolt", t.TempDir(), badYAML, "parse config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := server.NewDoltServer(tc.bin, tc.root, tc.cfg, "", 0, "")
			assert.Nil(t, s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestDoltServer_ID_Stable(t *testing.T) {
	cfgPath := writeConfig(t, 3306)
	rootA := t.TempDir()
	rootB := t.TempDir()

	a1, err := server.NewDoltServer("dolt", rootA, cfgPath, "", 0, "")
	require.NoError(t, err)
	a2, err := server.NewDoltServer("dolt", rootA, cfgPath, "", 0, "")
	require.NoError(t, err)
	b, err := server.NewDoltServer("dolt", rootB, cfgPath, "", 0, "")
	require.NoError(t, err)

	ctx := context.Background()
	assert.Equal(t, a1.ID(ctx), a2.ID(ctx), "same rootDir -> same ID")
	assert.NotEqual(t, a1.ID(ctx), b.ID(ctx), "different rootDir -> different ID")
}

func TestDoltServer_DSN(t *testing.T) {
	cfgPath := writeConfig(t, 13306)
	s, err := server.NewDoltServer("dolt", t.TempDir(), cfgPath, "", 0, "")
	require.NoError(t, err)

	ctx := context.Background()
	parsed, err := mysqldrv.ParseDSN(s.DSN(ctx, "", "alice", "s3cret"))
	require.NoError(t, err)
	assert.Equal(t, "alice", parsed.User)
	assert.Equal(t, "s3cret", parsed.Passwd)
	assert.Equal(t, "tcp", parsed.Net)
	assert.Equal(t, "127.0.0.1:13306", parsed.Addr)
	assert.Empty(t, parsed.DBName)
	assert.True(t, parsed.ParseTime)
	assert.True(t, parsed.MultiStatements)

	// database arg is independent of credentials.
	parsedDB, err := mysqldrv.ParseDSN(s.DSN(ctx, "mydb", "alice", "s3cret"))
	require.NoError(t, err)
	assert.Equal(t, "mydb", parsedDB.DBName)

	// Empty password is allowed.
	parsedEmpty, err := mysqldrv.ParseDSN(s.DSN(ctx, "", "root", ""))
	require.NoError(t, err)
	assert.Equal(t, "root", parsedEmpty.User)
	assert.Empty(t, parsedEmpty.Passwd)
}

func TestDoltServer_StartStop_HappyPath(t *testing.T) {
	s, rootDir := newDoltServer(t)
	ctx := context.Background()

	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { stopWithTimeout(t, s) })
	assert.True(t, s.Running(ctx))
	pf, err := pidfile.Read(rootDir, server.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, pf)
	require.NoError(t, pf.ValidateV2(pidfile.KindDoltBackend))
	match, err := procid.Verify(pf.Pid, procid.Token(pf.Birth))
	require.NoError(t, err)
	assert.True(t, match)

	db, err := sql.Open("mysql", s.DSN(ctx, "", "root", ""))
	require.NoError(t, err)
	var got int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT 1").Scan(&got))
	assert.Equal(t, 1, got)
	// Close the pool before stopping the server so COM_QUIT is sent while
	// the listener is still alive. Otherwise the driver logs broken-pipe
	// errors when it tries to drain the pool against a dead socket.
	require.NoError(t, db.Close())

	require.NoError(t, s.Stop(ctx))
	assert.False(t, s.Running(ctx))

	// Second Stop is a no-op.
	require.NoError(t, s.Stop(ctx))
}

func TestDoltServer_StartStop_UnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets not supported on windows")
	}
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()

	sock := filepath.Join(t.TempDir(), "s.sock")
	// Linux sun_path is 108 bytes including the NUL terminator; macOS is 104.
	// Skip on systems where t.TempDir() pushes us past the limit rather than
	// surface a confusing bind() error.
	if len(sock) >= 104 {
		t.Skipf("socket path too long (%d bytes): %s", len(sock), sock)
	}

	port := freePort(t)
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	body := fmt.Sprintf(`log_level: debug
listener:
  host: 127.0.0.1
  port: %d
  socket: %s
`, port, sock)
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))

	logPath := filepath.Join(t.TempDir(), "server.log")
	s, err := server.NewDoltServer(bin, rootDir, cfgPath, logPath, 0, "")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { stopWithTimeout(t, s) })
	assert.True(t, s.Running(ctx))

	// DSN must select the unix transport when a socket is configured.
	dsn := s.DSN(ctx, "", "root", "")
	parsed, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, "unix", parsed.Net, "DSN must use unix transport when socket is configured")
	assert.Equal(t, sock, parsed.Addr)

	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	var got int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT 1").Scan(&got))
	assert.Equal(t, 1, got)
	// Close the pool before stopping the server (see HappyPath comment).
	require.NoError(t, db.Close())

	// Dial uses the socket too.
	conn, err := s.Dial(ctx)
	require.NoError(t, err)
	assert.Equal(t, "unix", conn.RemoteAddr().Network())
	require.NoError(t, conn.Close())

	require.NoError(t, s.Stop(ctx))
	assert.False(t, s.Running(ctx))
}

func TestDoltServer_Stop_RunsGCWhenDatabaseSet(t *testing.T) {
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	port := freePort(t)
	cfg := writeConfig(t, port)
	log := filepath.Join(t.TempDir(), "server.log")

	const dbName = "gc_test_db"
	s, err := server.NewDoltServer(bin, rootDir, cfg, log, 0, dbName)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, s.Start(ctx))

	db, err := sql.Open("mysql", s.DSN(ctx, "", "root", ""))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+dbName)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	require.NoError(t, s.Stop(ctx))
	assert.False(t, s.Running(ctx))
}

func TestDoltServer_DoubleStart_Errors(t *testing.T) {
	s, _ := newDoltServer(t)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { stopWithTimeout(t, s) })

	err := s.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestDoltServer_StartStopStart_SameInstanceErrors(t *testing.T) {
	s, _ := newDoltServer(t)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx))
	require.NoError(t, s.Stop(ctx))

	// Same instance refuses to restart by design — Start is single-shot.
	err := s.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestDoltServer_StartStopStart_NewInstanceSameRootDirSucceeds(t *testing.T) {
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "server.log")
	ctx := context.Background()

	port1 := freePort(t)
	cfg1 := writeConfig(t, port1)
	s1, err := server.NewDoltServer(bin, rootDir, cfg1, logPath, 0, "")
	require.NoError(t, err)
	require.NoError(t, s1.Start(ctx))
	require.NoError(t, s1.Stop(ctx))

	// Fresh port to dodge any TIME_WAIT lingering on the old one.
	port2 := freePort(t)
	cfg2 := writeConfig(t, port2)
	s2, err := server.NewDoltServer(bin, rootDir, cfg2, logPath, 0, "")
	require.NoError(t, err)
	require.NoError(t, s2.Start(ctx), "new instance at same rootDir must start")
	t.Cleanup(func() { stopWithTimeout(t, s2) })
	assert.True(t, s2.Running(ctx))

	// ID is rootDir-derived and therefore stable across instances.
	assert.Equal(t, s1.ID(ctx), s2.ID(ctx))
}

func TestDoltServer_Dial_AfterStart(t *testing.T) {
	s, _ := newDoltServer(t)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { stopWithTimeout(t, s) })

	conn, err := s.Dial(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// The MySQL handshake packet starts with a 3-byte little-endian length
	// header + 1-byte sequence id, then a 1-byte protocol version. Modern
	// servers (incl. Dolt) emit protocol version 10. Reading these bytes
	// proves the listener is actually speaking MySQL, not just accepting TCP.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	var hdr [4]byte
	_, err = io.ReadFull(conn, hdr[:])
	require.NoError(t, err)
	payloadLen := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	require.Greater(t, payloadLen, 0)

	var proto [1]byte
	_, err = io.ReadFull(conn, proto[:])
	require.NoError(t, err)
	assert.Equal(t, byte(10), proto[0], "expected MySQL protocol version 10")
}

func TestDoltServer_Dial_BeforeStart(t *testing.T) {
	s, _ := newDoltServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Dial(ctx)
	require.Error(t, err, "Dial must fail before Start")
}

func TestDoltServer_LogFile_CapturesOutput(t *testing.T) {
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	port := freePort(t)
	cfgPath := writeConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	s, err := server.NewDoltServer(bin, rootDir, cfgPath, logPath, 0, "")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { stopWithTimeout(t, s) })
	require.NoError(t, s.Stop(ctx))

	info, err := os.Stat(logPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0), "log file should have captured server output")
}

func TestDoltServer_StopCancelsBeforeReady(t *testing.T) {
	s, _ := newDoltServer(t)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx))

	// Stop immediately, without waiting for ping. Must return within the
	// bounded window and leave Running == false.
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Stop(stopCtx) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(stopTimeout):
		t.Fatal("Stop did not return in time")
	}
	assert.False(t, s.Running(ctx))
}

func TestDoltServer_ConcurrentStart_SameRootDir_OneWins(t *testing.T) {
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())

	// Pre-configure dolt's global user.name/email so doltConfigure is a
	// no-op for every concurrent Start (no JSON-write race on
	// ~/.dolt/config_global.json).
	require.NoError(t, exec.Command(bin, "config", "--global", "--add", "user.name", "beads-test").Run())
	require.NoError(t, exec.Command(bin, "config", "--global", "--add", "user.email", "beads@test").Run())

	rootDir := t.TempDir()

	const n = 10
	servers := make([]*server.DoltServer, n)
	logDir := t.TempDir()
	for i := 0; i < n; i++ {
		port := freePort(t)
		cfg := writeConfig(t, port)
		log := filepath.Join(logDir, fmt.Sprintf("server-%d.log", i))
		s, err := server.NewDoltServer(bin, rootDir, cfg, log, 0, "")
		require.NoError(t, err)
		servers[i] = s
	}
	t.Cleanup(func() {
		for _, s := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			_ = s.Stop(ctx)
			cancel()
		}
	})

	var wg sync.WaitGroup
	startErrs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			startErrs[i] = servers[i].Start(context.Background())
		}(i)
	}
	wg.Wait()

	winner := -1
	losers := 0
	for i, err := range startErrs {
		if err == nil {
			require.Equal(t, -1, winner, "more than one Start succeeded (server %d and %d)", winner, i)
			winner = i
			continue
		}
		require.True(t, errors.Is(err, lockfile.ErrLocked),
			"server %d: expected proxy-child.lock contention, got %v", i, err)
		losers++
	}
	require.GreaterOrEqual(t, winner, 0, "no Start succeeded")
	assert.Equal(t, n-1, losers, "the other %d Starts must lose", n-1)

	assert.True(t, servers[winner].Running(context.Background()), "winner must be running")
	conn, err := servers[winner].Dial(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestDoltServer_DoltInit_Idempotent(t *testing.T) {
	bin := requireDolt(t)
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()

	// Pre-init the dolt repo manually. dolt init refuses to run without
	// user.name/email in global config, so set those first.
	require.NoError(t, exec.Command(bin, "config", "--global", "--add", "user.name", "beads-test").Run())
	require.NoError(t, exec.Command(bin, "config", "--global", "--add", "user.email", "beads@test").Run())

	cmd := exec.Command(bin, "init")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "manual dolt init failed: %s", out)

	port := freePort(t)
	cfgPath := writeConfig(t, port)
	s, err := server.NewDoltServer(bin, rootDir, cfgPath, "", 0, "")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, s.Start(ctx), "Start against pre-initialized rootDir should succeed")
	t.Cleanup(func() { stopWithTimeout(t, s) })
}
