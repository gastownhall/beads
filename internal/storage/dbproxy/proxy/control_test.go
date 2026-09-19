package proxy

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newControlServer(t *testing.T) (*controlServer, string, identity.IdentReply) {
	t.Helper()
	root := t.TempDir()
	secret, err := identity.WriteSecret(root)
	require.NoError(t, err)
	want := identity.IdentReply{
		Schema:     pidfile.SchemaV2,
		Role:       pidfile.KindProxy,
		RootID:     "workspace-root",
		UpstreamID: "dolt-server",
		PID:        123,
		Birth:      "linux-v1:boot:1",
		DataPort:   3306,
	}
	control, err := startControl(root, func() identity.IdentReply { return want })
	require.NoError(t, err)
	want.ControlPort = control.Port()
	t.Cleanup(func() { _ = control.Close() })
	return control, secret, want
}

func TestControl_Identify(t *testing.T) {
	control, secret, want := newControlServer(t)
	got, err := identity.Identify("127.0.0.1", control.Port(), secret, time.Second)
	require.NoError(t, err)
	assert.Len(t, got.MAC, 64)
	got.MAC = ""
	assert.Equal(t, &want, got)
}

func TestControl_RejectsWrongSecret(t *testing.T) {
	control, _, _ := newControlServer(t)
	_, err := identity.Identify("127.0.0.1", control.Port(), "not-the-secret", time.Second)
	require.ErrorIs(t, err, identity.ErrIdentRefused)
}

func TestControl_RejectsOversizedAndGarbageRequests(t *testing.T) {
	control, _, _ := newControlServer(t)
	cases := []struct {
		name    string
		request string
	}{
		{name: "oversized", request: "IDENT " + strings.Repeat("x", maxIdentRequestBytes) + "\n"},
		{name: "garbage", request: "WHOAMI\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port())), time.Second)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			_, err = io.WriteString(conn, tc.request)
			require.NoError(t, err)
			buf := make([]byte, 1)
			_, err = conn.Read(buf)
			assert.Error(t, err)
		})
	}
}

func TestControl_ConcurrentIdentify(t *testing.T) {
	control, secret, want := newControlServer(t)
	const calls = maxConcurrentIdentRequests
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			got, err := identity.Identify("127.0.0.1", control.Port(), secret, time.Second)
			if err == nil {
				got.MAC = ""
				if *got != want {
					err = errors.New("identity reply mismatch")
				}
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestControl_CapsConcurrentHandshakes(t *testing.T) {
	control, _, _ := newControlServer(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port()))
	conns := make([]net.Conn, 0, maxConcurrentIdentRequests)
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	for range maxConcurrentIdentRequests {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		conns = append(conns, conn)
	}
	require.Eventually(t, func() bool {
		return len(control.slots) == maxConcurrentIdentRequests
	}, time.Second, 10*time.Millisecond)

	extra, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	defer extra.Close()
	require.NoError(t, extra.SetDeadline(time.Now().Add(time.Second)))
	_, err = io.WriteString(extra, "IDENT blocked blocked\n")
	require.NoError(t, err)
	buf := make([]byte, 1)
	_, err = extra.Read(buf)
	require.Error(t, err)
}

// Persistent transient Accept failures (e.g. EMFILE) must not tear down the
// control server: the data path may still be healthy, and losing control only
// degrades adoption to the caller's poll/retry path.
func TestControl_AcceptLoopSurvivesPersistentErrors(t *testing.T) {
	listener := &failingListener{err: errors.New("transient accept failure"), failures: 11}
	control := &controlServer{
		listener: listener,
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
		slots:    make(chan struct{}, maxConcurrentIdentRequests),
	}
	immediately := make(chan time.Time)
	close(immediately)
	recorder := &acceptRetryRecorder{afterResult: immediately}
	go control.acceptLoopWithAfter(recorder.after)

	select {
	case <-control.done:
	case <-time.After(time.Second):
		t.Fatal("control accept loop did not exit after the listener closed")
	}
	assert.Equal(t, listener.failures+1, listener.accepts,
		"loop must retry through every transient failure until the listener reports closed")
	assert.ErrorIs(t, listener.finalErr, net.ErrClosed)
	assert.False(t, listener.closed, "accept failures must not close the control listener")
	assert.Equal(t, []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		160 * time.Millisecond,
		320 * time.Millisecond,
		640 * time.Millisecond,
		1280 * time.Millisecond,
		2560 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}, recorder.delays)
}

type acceptRetryRecorder struct {
	afterResult <-chan time.Time
	delays      []time.Duration
}

func (r *acceptRetryRecorder) after(delay time.Duration) <-chan time.Time {
	r.delays = append(r.delays, delay)
	return r.afterResult
}

type failingListener struct {
	err      error
	failures int
	accepts  int
	finalErr error
	closed   bool
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.accepts++
	if l.accepts <= l.failures {
		return nil, l.err
	}
	l.finalErr = net.ErrClosed
	return nil, l.finalErr
}

func (l *failingListener) Close() error {
	l.closed = true
	return nil
}

func (l *failingListener) Addr() net.Addr {
	return &net.TCPAddr{}
}
