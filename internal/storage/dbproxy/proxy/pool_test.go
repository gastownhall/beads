package proxy

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeBackend is an in-memory MySQL-speaking stub: it completes the server side
// of the handshake and answers COM_PING / COM_RESET_CONNECTION with OK. It lets
// the pool be exercised without a real dolt sql-server. Counters are shared
// across all connections a dialer produces.
type fakeBackend struct {
	dials  atomic.Int64
	resets atomic.Int64
	pings  atomic.Int64
}

// dialer returns a poolDialer that spins up a fresh stub connection per dial.
func (f *fakeBackend) dialer() poolDialer {
	return func(ctx context.Context) (net.Conn, error) {
		f.dials.Add(1)
		proxySide, backendSide := net.Pipe()
		go f.serve(backendSide)
		return proxySide, nil
	}
}

func (f *fakeBackend) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	// Server greeting (seq 0).
	if err := writePacket(c, 0, serverGreeting(1, makeSalt(1))); err != nil {
		return
	}
	// Read client handshake response (seq 1); accept anything.
	resp, _, err := readPacket(c)
	if err != nil {
		return
	}
	if _, perr := parseClientHandshakeResponse(resp); perr != nil {
		_ = writePacket(c, 2, []byte{0xff, 0x15, 0x04}) // ERR
		return
	}
	if err := writePacket(c, 2, okPacket(capProtocol41)); err != nil { // auth OK (seq 2)
		return
	}
	// Command loop.
	for {
		pkt, seq, err := readPacket(c)
		if err != nil {
			return
		}
		if len(pkt) == 0 {
			return
		}
		switch pkt[0] {
		case comResetConnection:
			f.resets.Add(1)
			if err := writePacket(c, seq+1, okPacket(capProtocol41)); err != nil {
				return
			}
		case comPing:
			f.pings.Add(1)
			if err := writePacket(c, seq+1, okPacket(capProtocol41)); err != nil {
				return
			}
		case 0x01: // COM_QUIT
			return
		default:
			_ = writePacket(c, seq+1, okPacket(capProtocol41))
		}
	}
}

func testKey() backendKey {
	return backendKey{caps: capProtocol41 | capSecureConnection | capPluginAuth, db: "beads"}
}

func TestPool_GetDialsAndPutReturns(t *testing.T) {
	fb := &fakeBackend{}
	stats := &Stats{}
	p := newBackendPool(fb.dialer(), "root", "", 4, 0, stats)
	defer p.drain()

	ctx := context.Background()
	pc, err := p.get(ctx, testKey())
	require.NoError(t, err)
	require.Equal(t, int64(1), fb.dials.Load(), "first get must dial")
	require.Equal(t, 0, p.idleCount())

	p.put(pc)
	require.Equal(t, int64(1), fb.resets.Load(), "put must COM_RESET_CONNECTION")
	require.Equal(t, 1, p.idleCount(), "reset conn returns to pool")

	// Second get reuses the idle connection (no new dial).
	pc2, err := p.get(ctx, testKey())
	require.NoError(t, err)
	require.Equal(t, int64(1), fb.dials.Load(), "reuse must not dial")
	require.GreaterOrEqual(t, fb.pings.Load(), int64(1), "reuse pings for liveness")
	p.put(pc2)

	snap := stats.Snapshot()
	require.GreaterOrEqual(t, snap.PoolHits, int64(1))
	require.GreaterOrEqual(t, snap.PoolMisses, int64(1))
	require.GreaterOrEqual(t, snap.PoolResets, int64(2))
}

func TestPool_MaxIdleCap(t *testing.T) {
	fb := &fakeBackend{}
	p := newBackendPool(fb.dialer(), "root", "", 2, 0, &Stats{})
	defer p.drain()
	ctx := context.Background()

	// Borrow three concurrently, then return all three. Only maxIdle=2 are kept.
	a, err := p.get(ctx, testKey())
	require.NoError(t, err)
	b, err := p.get(ctx, testKey())
	require.NoError(t, err)
	c, err := p.get(ctx, testKey())
	require.NoError(t, err)
	require.Equal(t, int64(3), fb.dials.Load())

	p.put(a)
	p.put(b)
	p.put(c)
	require.Equal(t, 2, p.idleCount(), "idle retention capped at maxIdle")
}

func TestPool_KeyIsolation(t *testing.T) {
	fb := &fakeBackend{}
	p := newBackendPool(fb.dialer(), "root", "", 4, 0, &Stats{})
	defer p.drain()
	ctx := context.Background()

	k1 := backendKey{caps: capProtocol41, db: "alpha"}
	k2 := backendKey{caps: capProtocol41, db: "beta"}

	pc1, err := p.get(ctx, k1)
	require.NoError(t, err)
	p.put(pc1)

	// A get for a different key must NOT reuse k1's idle connection.
	pc2, err := p.get(ctx, k2)
	require.NoError(t, err)
	require.Equal(t, int64(2), fb.dials.Load(), "different key dials a fresh backend")
	p.put(pc2)
}

func TestPool_DrainClosesIdle(t *testing.T) {
	fb := &fakeBackend{}
	p := newBackendPool(fb.dialer(), "root", "", 4, 0, &Stats{})
	ctx := context.Background()
	pc, err := p.get(ctx, testKey())
	require.NoError(t, err)
	p.put(pc)
	require.Equal(t, 1, p.idleCount())

	p.drain()
	require.Equal(t, 0, p.idleCount())

	// get after drain fails.
	_, err = p.get(ctx, testKey())
	require.ErrorIs(t, err, errPoolClosed)
}

func TestPool_DeadIdleConnDiscarded(t *testing.T) {
	fb := &fakeBackend{}
	p := newBackendPool(fb.dialer(), "root", "", 4, 0, &Stats{})
	defer p.drain()
	ctx := context.Background()

	pc, err := p.get(ctx, testKey())
	require.NoError(t, err)
	p.put(pc)
	require.Equal(t, 1, p.idleCount())

	// Kill the idle connection out from under the pool.
	pc.conn.Close()

	// Next get must detect the dead conn (ping fails) and dial a fresh one.
	pc2, err := p.get(ctx, testKey())
	require.NoError(t, err)
	require.Equal(t, int64(2), fb.dials.Load(), "dead idle conn replaced by a fresh dial")
	p.put(pc2)
}

func TestPool_LifetimeRetire(t *testing.T) {
	fb := &fakeBackend{}
	p := newBackendPool(fb.dialer(), "root", "", 4, 10*time.Millisecond, &Stats{})
	defer p.drain()
	ctx := context.Background()

	pc, err := p.get(ctx, testKey())
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	p.put(pc) // expired → destroyed, not returned
	require.Equal(t, 0, p.idleCount(), "expired conn not retained")
}
