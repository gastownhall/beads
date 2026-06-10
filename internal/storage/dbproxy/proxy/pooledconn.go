package proxy

// handlePooledSession drives one client connection against a pooled backend:
// it terminates the client handshake (skip-auth), borrows a wire-compatible
// backend connection, forwards the command phase byte-transparently in both
// directions, and returns the backend to the pool (reset) when the client
// disconnects. This is the pooling counterpart to the transparent
// dial+io.Copy+Close path in proxyServer.handleConn.

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// handshakeTimeout bounds the client+backend handshake phase. The command
// phase that follows is unbounded (long-lived bd sessions are normal).
const handshakeTimeout = 10 * time.Second

// sessionResult reports what the pool should do with the backend afterward.
type sessionResult struct {
	backend  *pooledConn
	reusable bool // false → caller must destroy (don't return to pool)
}

// runPooledSession performs the handshake handoff and bidirectional copy. It
// returns the borrowed backend and whether it is safe to return to the pool.
// The caller owns closing the client connection.
func runPooledSession(ctx context.Context, stats *Stats, pool *backendPool, client net.Conn, connID uint32) (sessionResult, error) {
	hsDeadline := time.Now().Add(handshakeTimeout)
	ch, err := acceptClient(client, connID, hsDeadline)
	if err != nil {
		return sessionResult{}, err
	}

	key := backendKey{caps: ch.capabilities, db: ch.database}
	getCtx, cancel := context.WithDeadline(ctx, hsDeadline)
	pc, err := pool.get(getCtx, key)
	cancel()
	if err != nil {
		return sessionResult{}, err
	}

	// Command phase. The backend→client direction is byte-transparent
	// (io.Copy), exactly like handleConn. The client→backend direction is
	// frame-aware so it can intercept COM_QUIT: go-sql-driver sends COM_QUIT
	// when it closes a connection, and forwarding that to a pooled backend
	// would terminate it — defeating pooling. We swallow COM_QUIT, end the
	// session, reset the backend, and return it to the pool instead.
	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }

	var g sync.WaitGroup
	var c2bErr error
	var sawQuit bool

	g.Add(1)
	go func() {
		defer g.Done()
		defer finish()
		var n int64
		n, sawQuit, c2bErr = copyClientCommands(pc.conn, client)
		stats.AddBytesClientToBackend(n)
	}()

	g.Add(1)
	go func() {
		defer g.Done()
		n, _ := io.Copy(client, pc.conn)
		stats.AddBytesBackendToClient(n)
	}()

	// When the client→backend direction finishes (client closed its write
	// side / disconnected), unblock the backend→client copy by setting an
	// immediate read deadline on the backend so io.Copy returns and we can
	// reclaim the connection for the pool.
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = pc.conn.SetReadDeadline(time.Now())
	}()

	g.Wait()
	_ = pc.conn.SetReadDeadline(time.Time{}) // clear for reuse

	// Determine reusability. The safe case is a clean COM_QUIT from the client:
	// the backend is then guaranteed to be at a command boundary. A plain EOF
	// (client vanished without COM_QUIT) may have happened mid-command, so the
	// backend stream could be misaligned — the reset's sequence-id check
	// (resetBackend) is the backstop, but we conservatively destroy on
	// non-quit endings and on context cancellation.
	reusable := backendReusable(ctx, sawQuit, c2bErr)
	return sessionResult{backend: pc, reusable: reusable}, nil
}

// copyClientCommands forwards client→backend bytes frame by frame, intercepting
// a standalone COM_QUIT (which go-sql-driver emits on Close) so it is not
// delivered to the pooled backend. It returns the byte count forwarded, whether
// a COM_QUIT was seen, and any transport error.
func copyClientCommands(backend, client net.Conn) (int64, bool, error) {
	var total int64
	var hdr [4]byte
	atCommandStart := true
	for {
		if _, err := io.ReadFull(client, hdr[:]); err != nil {
			if isExpectedClose(err) {
				return total, false, nil
			}
			return total, false, err
		}
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		seq := hdr[3]
		payload := make([]byte, n)
		if n > 0 {
			if _, err := io.ReadFull(client, payload); err != nil {
				return total, false, err
			}
		}
		// A new command begins at seq 0 when we are not mid-multiframe. A
		// standalone COM_QUIT is exactly one byte (0x01).
		if atCommandStart && seq == 0 && n == 1 && payload[0] == 0x01 {
			return total, true, nil
		}
		if _, err := backend.Write(hdr[:]); err != nil {
			return total, false, err
		}
		if n > 0 {
			if _, err := backend.Write(payload); err != nil {
				return total, false, err
			}
		}
		total += int64(4 + n)
		// The next frame starts a new command unless this was a max-size frame
		// (0xffffff) that continues into another frame.
		atCommandStart = n < 0xffffff
	}
}

// backendReusable decides whether the backend connection can be safely reset
// and returned to the pool after a session ends.
func backendReusable(ctx context.Context, sawQuit bool, c2bErr error) bool {
	if ctx.Err() != nil {
		return false
	}
	if c2bErr != nil {
		return false
	}
	return sawQuit
}

func isExpectedClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}
