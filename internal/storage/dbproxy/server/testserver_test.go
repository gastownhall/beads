package server_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ioDeadline = 2 * time.Second

func mustWrite(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	require.NoError(t, c.SetWriteDeadline(time.Now().Add(ioDeadline)))
	n, err := c.Write([]byte(msg))
	require.NoError(t, err)
	require.Equal(t, len(msg), n)
}

func mustReadN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(ioDeadline)))
	buf := make([]byte, n)
	got, err := io.ReadFull(c, buf)
	require.NoError(t, err)
	require.Equal(t, n, got)
	return buf
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(ioDeadline)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("eventually: %s", msg)
}

func TestNew_Defaults(t *testing.T) {
	srv := server.New()

	assert.Equal(t, "test-server", srv.ID_)
	assert.Equal(t, "test://in-memory", srv.DSN_)
	assert.NotNil(t, srv.Handler)
	assert.Equal(t, server.Counters{}, srv.Snapshot())
}

func TestID_DSN_CountAndReturn(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.ID_ = "custom-id"
	srv.DSN_ = "custom://dsn"

	for i := 0; i < 3; i++ {
		assert.Equal(t, "custom-id", srv.ID(ctx))
		assert.Equal(t, "custom://dsn", srv.DSN(ctx, "", "", ""))
	}

	c := srv.Snapshot()
	assert.Equal(t, int64(3), c.IDCalls)
	assert.Equal(t, int64(3), c.DSNCalls)
}

func TestStart_Success(t *testing.T) {
	ctx := context.Background()
	srv := server.New()

	require.NoError(t, srv.Start(ctx))
	assert.Equal(t, int64(1), srv.Snapshot().StartCalls)
	assert.True(t, srv.Running(ctx))
}

func TestStart_Error(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.StartErr = errors.New("boom")

	assert.EqualError(t, srv.Start(ctx), "boom")
	assert.Equal(t, int64(1), srv.Snapshot().StartCalls)
	assert.False(t, srv.Running(ctx))
}

func TestStop_Success(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	require.NoError(t, srv.Start(ctx))

	require.NoError(t, srv.Stop(ctx))
	assert.Equal(t, int64(1), srv.Snapshot().StopCalls)
	assert.False(t, srv.Running(ctx))
}

func TestStop_Error(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.StopErr = errors.New("nope")

	assert.EqualError(t, srv.Stop(ctx), "nope")
	assert.Equal(t, int64(1), srv.Snapshot().StopCalls)
}

func TestRunning_Counter(t *testing.T) {
	ctx := context.Background()
	srv := server.New()

	for i := 0; i < 5; i++ {
		_ = srv.Running(ctx)
	}
	assert.Equal(t, int64(5), srv.Snapshot().RunningCalls)
}

func TestDial_Echo(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn, err := srv.Dial(ctx)
	require.NoError(t, err)

	c := srv.Snapshot()
	assert.Equal(t, int64(1), c.DialCalls)
	assert.Equal(t, int64(1), c.AcceptedConns)
	assert.Equal(t, int64(1), c.OpenConns)

	mustWrite(t, conn, "hello")
	got := mustReadN(t, conn, 5)
	assert.Equal(t, "hello", string(got))

	require.NoError(t, conn.Close())

	eventually(t, func() bool { return srv.Snapshot().OpenConns == 0 }, "OpenConns did not drain")

	c = srv.Snapshot()
	assert.Equal(t, int64(5), c.BytesIn)
	assert.Equal(t, int64(5), c.BytesOut)
}

func TestDial_Error(t *testing.T) {
	srv := server.New()
	srv.DialErr = errors.New("refused")

	conn, err := srv.Dial(context.Background())
	assert.Nil(t, conn)
	assert.EqualError(t, err, "refused")

	c := srv.Snapshot()
	assert.Equal(t, int64(1), c.DialCalls)
	assert.Equal(t, int64(1), c.DialErrors)
	assert.Equal(t, int64(0), c.AcceptedConns)
	assert.Equal(t, int64(0), c.OpenConns)
}

func TestDial_DiscardHandler(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.Handler = server.DiscardHandler
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn, err := srv.Dial(ctx)
	require.NoError(t, err)

	mustWrite(t, conn, "0123456789")
	require.NoError(t, conn.Close())

	eventually(t, func() bool { return srv.Snapshot().OpenConns == 0 }, "OpenConns did not drain")

	c := srv.Snapshot()
	assert.Equal(t, int64(10), c.BytesIn)
	assert.Equal(t, int64(0), c.BytesOut)
}

func TestDial_CustomHandler(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	payload := "from-backend"
	srv.Handler = func(c net.Conn) {
		defer c.Close()
		_, _ = c.Write([]byte(payload))
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn, err := srv.Dial(ctx)
	require.NoError(t, err)

	got := mustReadN(t, conn, len(payload))
	assert.Equal(t, payload, string(got))

	require.NoError(t, conn.Close())
	eventually(t, func() bool { return srv.Snapshot().OpenConns == 0 }, "OpenConns did not drain")

	c := srv.Snapshot()
	assert.Equal(t, int64(0), c.BytesIn)
	assert.Equal(t, int64(len(payload)), c.BytesOut)
}

func TestStop_ClosesOpenConns(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.Handler = server.DiscardHandler
	require.NoError(t, srv.Start(ctx))

	conn1, err := srv.Dial(ctx)
	require.NoError(t, err)
	conn2, err := srv.Dial(ctx)
	require.NoError(t, err)

	require.NoError(t, srv.Stop(ctx))

	// Stop closed the backend side of each pipe, so proxy-side Read returns
	// EOF immediately — no deadline needed (and SetReadDeadline on a pipe
	// with a closed remote itself returns io.ErrClosedPipe).
	_, err = conn1.Read(make([]byte, 1))
	assert.Error(t, err)
	_, err = conn2.Read(make([]byte, 1))
	assert.Error(t, err)

	eventually(t, func() bool { return srv.Snapshot().OpenConns == 0 }, "OpenConns did not drain after Stop")
}

func TestSnapshot_IsValueCopy(t *testing.T) {
	srv := server.New()
	require.NoError(t, srv.Start(context.Background()))

	snap := srv.Snapshot()
	snap.StartCalls = 9999

	assert.Equal(t, int64(1), srv.Snapshot().StartCalls)
}

func TestDial_ConcurrentTracking(t *testing.T) {
	ctx := context.Background()
	srv := server.New()
	srv.Handler = server.DiscardHandler
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	const n = 10
	conns := make([]net.Conn, n)
	var wg sync.WaitGroup
	var dialErrs atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := srv.Dial(ctx)
			if err != nil {
				dialErrs.Add(1)
				return
			}
			conns[i] = c
		}(i)
	}
	wg.Wait()
	require.Equal(t, int64(0), dialErrs.Load())

	eventually(t, func() bool {
		c := srv.Snapshot()
		return c.OpenConns == n && c.AcceptedConns == n
	}, "did not reach n=10 open conns")

	for _, c := range conns {
		_ = c.Close()
	}

	eventually(t, func() bool { return srv.Snapshot().OpenConns == 0 }, "OpenConns did not drain")
	assert.Equal(t, int64(n), srv.Snapshot().AcceptedConns)
}
