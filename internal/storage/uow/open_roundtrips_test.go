package uow

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
)

type countingProxy struct {
	addr     string
	conns    atomic.Int32
	awaited  atomic.Int32
	mu       sync.Mutex
	sessions map[int32]int
	trace    []string
}

func startCountingProxy(t *testing.T, backend string) *countingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &countingProxy{addr: ln.Addr().String(), sessions: map[int32]int{}}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.conns.Add(1)
			go p.serve(c, backend)
		}
	}()
	return p
}

func (p *countingProxy) serve(client net.Conn, backend string) {
	id := p.conns.Load()
	defer client.Close()
	server, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer server.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, server)
		_ = client.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		defer func() { _ = server.(*net.TCPConn).CloseWrite() }()
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(client, hdr[:]); err != nil {
				return
			}
			n := int(binary.LittleEndian.Uint32([]byte{hdr[0], hdr[1], hdr[2], 0}))
			payload := make([]byte, n)
			if _, err := io.ReadFull(client, payload); err != nil {
				return
			}
			if hdr[3] == 0 && n > 0 {
				switch payload[0] {
				case 0x01, 0x19: // COM_QUIT, COM_STMT_CLOSE
				default:
					p.awaited.Add(1)
					text := string(payload[1:])
					if len(text) > 70 {
						text = text[:70]
					}
					p.mu.Lock()
					p.sessions[id]++
					p.trace = append(p.trace, fmt.Sprintf("conn#%d cmd=0x%02x %q", id, payload[0], text))
					p.mu.Unlock()
				}
			}
			if _, err := server.Write(append(hdr[:], payload...)); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}

func (p *countingProxy) reset() (conns, sessions, awaited int32) {
	p.mu.Lock()
	sessions = int32(len(p.sessions))
	p.sessions = map[int32]int{}
	p.trace = nil
	p.mu.Unlock()
	return p.conns.Swap(0), sessions, p.awaited.Swap(0)
}

func (p *countingProxy) dump(t *testing.T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.trace {
		t.Log(line)
	}
}

func TestOpenAndInitSchema_ExistingDatabaseUsesOneConnection(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	cp := startCountingProxy(t, net.JoinHostPort("127.0.0.1", port))
	proxyHost, proxyPortStr, err := net.SplitHostPort(cp.addr)
	require.NoError(t, err)
	proxyPort, err := strconv.Atoi(proxyPortStr)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })
	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() { _ = proxy.Shutdown(storeRootDir) })
	logPath := filepath.Join(t.TempDir(), "server.log")
	external := configfile.ExternalDoltConfig{Host: proxyHost, Port: proxyPort}

	open := func(teamServer bool, projectID string) *doltSQLProvider {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		provider, err := NewExternalDoltServerUOWProvider(ctx, storeRootDir, "beads_fresh", logPath,
			external, "root", "", 0, 0, teamServer, projectID)
		require.NoError(t, err)
		sqlProv, ok := provider.(*doltSQLProvider)
		require.True(t, ok, "expected *doltSQLProvider, got %T", provider)
		return sqlProv
	}

	cp.reset()
	first := open(false, "")
	conns, sessions, awaited := cp.reset()
	t.Logf("fresh database: %d connections, %d sessions, %d awaited commands", conns, sessions, awaited)
	_, err = first.db.ExecContext(context.Background(),
		"INSERT INTO metadata (`key`, value) VALUES ('_project_id', 'proj-1')")
	require.NoError(t, err)
	require.NoError(t, first.Close(context.Background()))

	cp.reset()
	second := open(false, "")
	if cp.sessionCount() != 1 {
		cp.dump(t)
	}
	conns, sessions, awaited = cp.reset()
	t.Logf("existing database: %d connections, %d sessions, %d awaited commands", conns, sessions, awaited)
	require.Equal(t, int32(1), sessions)
	require.NoError(t, second.Close(context.Background()))

	third := open(true, "proj-1")
	if cp.sessionCount() != 1 {
		cp.dump(t)
	}
	conns, sessions, awaited = cp.reset()
	t.Logf("team-server: %d connections, %d sessions, %d awaited commands", conns, sessions, awaited)
	require.Equal(t, int32(1), sessions)
	require.LessOrEqual(t, awaited, int32(2), "team-server open should be the driver's connect probe plus one SELECT")
	require.NoError(t, third.Close(context.Background()))
}

func (p *countingProxy) sessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}
