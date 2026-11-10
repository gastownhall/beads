package uow

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// TestProxyEndpointResolverReplacesOnlyAddress pins what a rediscovered
// connection keeps from the provider's open: everything in the DSN except the
// proxy address, including an external upstream's TLS config and the server
// name it verifies, which names the upstream rather than the local proxy.
func TestProxyEndpointResolverReplacesOnlyAddress(t *testing.T) {
	const tlsName = "uow-test-rediscovery-tls"
	require.NoError(t, mysql.RegisterTLSConfig(tlsName, &tls.Config{ServerName: "db.example.test", MinVersion: tls.VersionTLS12}))
	t.Cleanup(func() { mysql.DeregisterTLSConfig(tlsName) })

	bootstrap := proxy.Endpoint{Host: "127.0.0.1", Port: 4000}
	cfg, err := mysql.ParseDSN(buildDSN(proxy.DataNetwork, bootstrap, "beads", "operator", "s3cret", tlsName))
	require.NoError(t, err)
	require.NotNil(t, cfg.TLS)

	r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
		return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
	})
	r.arm()
	got := cfg.Clone()
	require.NoError(t, r.beforeConnect(context.Background(), got))

	want := cfg.Clone()
	want.Addr = "127.0.0.1:4321"
	assert.Equal(t, want.FormatDSN(), got.FormatDSN())
	assert.Equal(t, proxy.DataNetwork, got.Net, "the connection must still dial through the authenticated proxy network")
	assert.Equal(t, "beads", got.DBName)
	assert.Equal(t, "operator", got.User)
	assert.Equal(t, "s3cret", got.Passwd)
	assert.Equal(t, 5*time.Second, got.Timeout)
	assert.True(t, got.ClientFoundRows)
	require.NotNil(t, got.TLS)
	assert.Equal(t, "db.example.test", got.TLS.ServerName)
	assert.Equal(t, "127.0.0.1:4000", cfg.Addr, "the provider's own config must not change")
}

// waitProbeContext reports on waiting, once, when a hook first asks for its
// Done channel: the point at which it waits on a discovery it did not run
// itself.
type waitProbeContext struct {
	context.Context
	once    sync.Once
	waiting chan<- struct{}
}

func (c *waitProbeContext) Done() <-chan struct{} {
	c.once.Do(func() { c.waiting <- struct{}{} })
	return c.Context.Done()
}

// receiveWithin returns the next value from ch, failing the test if none
// arrives within d.
func receiveWithin[T any](t *testing.T, ch <-chan T, d time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for %s", d, what)
		var zero T
		return zero
	}
}

// TestProxyEndpointResolverCoalesces pins that hooks overlapping one blocked
// discovery share it: it runs once, every waiter gets its endpoint, and the
// result is not cached for a later connection. The overlap's discovery and any
// later one answer with different endpoints, so a waiter that ran its own
// discovery, or a later connection served from a cache, dials the wrong address.
func TestProxyEndpointResolverCoalesces(t *testing.T) {
	const (
		hooks      = 4
		sharedPort = 4321 // the one blocked discovery every waiter must join
		freshPort  = 4322 // what a discovery after the overlap returns
	)
	var calls atomic.Int64
	entered := make(chan struct{}, hooks+1)
	release := make(chan struct{})
	r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
		n := calls.Add(1)
		entered <- struct{}{}
		<-release
		if n == 1 {
			return proxy.Endpoint{Host: "127.0.0.1", Port: sharedPort}, nil
		}
		return proxy.Endpoint{Host: "127.0.0.1", Port: freshPort}, nil
	})
	r.arm()

	waiting := make(chan struct{}, hooks)
	results := make(chan error, hooks)
	cfgs := make([]*mysql.Config, hooks)
	for i := range cfgs {
		cfgs[i] = mysql.NewConfig()
		cfgs[i].Addr = "127.0.0.1:4000"
		ctx := &waitProbeContext{Context: context.Background(), waiting: waiting}
		go func(cfg *mysql.Config) { results <- r.beforeConnect(ctx, cfg) }(cfgs[i])
	}
	// Release discovery only once every hook is inside the overlap: waiting on
	// the shared discovery or, were there no sharing, running its own.
	deadline := time.After(10 * time.Second)
	for waited, ran := 0, 0; waited < hooks && ran < hooks; {
		select {
		case <-waiting:
			waited++
		case <-entered:
			ran++
		case <-deadline:
			t.Fatalf("hooks never all reached discovery: %d waiting, %d discovering", waited, ran)
		}
	}
	close(release)
	for range hooks {
		require.NoError(t, receiveWithin(t, results, 10*time.Second, "a hook"))
	}
	assert.Equal(t, int64(1), calls.Load(), "overlapping hooks must share one discovery")
	for i, cfg := range cfgs {
		assert.Equal(t, "127.0.0.1:4321", cfg.Addr, "hook %d", i)
	}

	later := mysql.NewConfig()
	require.NoError(t, r.beforeConnect(context.Background(), later))
	assert.Equal(t, int64(2), calls.Load(), "a connection after the overlap must discover again, not reuse a cached endpoint")
	assert.Equal(t, "127.0.0.1:4322", later.Addr, "a connection after the overlap must dial the fresh discovery's endpoint")
}

// blockedDiscovery is a discovery that blocks until released, recording how
// often it ran and whether its last run finished.
type blockedDiscovery struct {
	calls    atomic.Int64
	finished atomic.Int64
	entered  chan struct{}
	release  chan struct{}
}

func newBlockedDiscovery() *blockedDiscovery {
	return &blockedDiscovery{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (b *blockedDiscovery) discover() (proxy.Endpoint, error) {
	b.calls.Add(1)
	b.entered <- struct{}{}
	<-b.release
	b.finished.Add(1)
	return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
}

// gatedDoneContext reports on waiting when a hook first asks for its Done
// channel, then holds the hook there until gate closes, so a test can make
// cancellation and a finished discovery ready at the same moment.
type gatedDoneContext struct {
	context.Context
	once    sync.Once
	waiting chan<- struct{}
	gate    <-chan struct{}
}

func (c *gatedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() {
		c.waiting <- struct{}{}
		<-c.gate
	})
	return c.Context.Done()
}

// TestProxyEndpointResolverCancellation pins that a caller's cancellation ends
// only that caller's wait: it returns its own context error promptly, the
// discovery it shared keeps running (a spawned proxy is not a caller's to
// stop), and the other waiters still get its endpoint.
func TestProxyEndpointResolverCancellation(t *testing.T) {
	t.Run("already canceled caller starts no discovery", func(t *testing.T) {
		b := newBlockedDiscovery()
		r := newProxyEndpointResolver(b.discover)
		r.arm()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cfg := mysql.NewConfig()
		cfg.Addr = "127.0.0.1:4000"
		require.ErrorIs(t, r.beforeConnect(ctx, cfg), context.Canceled)
		assert.Zero(t, b.calls.Load())
		assert.Equal(t, "127.0.0.1:4000", cfg.Addr)
	})

	t.Run("one of two waiters cancels", func(t *testing.T) {
		b := newBlockedDiscovery()
		r := newProxyEndpointResolver(b.discover)
		r.arm()
		waiting := make(chan struct{}, 2)
		canceledCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		canceledCfg, otherCfg := mysql.NewConfig(), mysql.NewConfig()
		canceledDone, otherDone := make(chan error, 1), make(chan error, 1)
		// The canceling caller is the one whose connection started discovery.
		go func() {
			canceledDone <- r.beforeConnect(&waitProbeContext{Context: canceledCtx, waiting: waiting}, canceledCfg)
		}()
		receiveWithin(t, b.entered, 10*time.Second, "discovery to start")
		receiveWithin(t, waiting, 10*time.Second, "the first caller to wait")
		go func() {
			otherDone <- r.beforeConnect(&waitProbeContext{Context: context.Background(), waiting: waiting}, otherCfg)
		}()
		receiveWithin(t, waiting, 10*time.Second, "the second caller to wait")

		cancel()
		require.ErrorIs(t, receiveWithin(t, canceledDone, time.Second, "the canceled caller"), context.Canceled)
		assert.Zero(t, b.finished.Load(), "the canceled caller must return while discovery is still running")

		close(b.release)
		require.NoError(t, receiveWithin(t, otherDone, 10*time.Second, "the remaining caller"))
		assert.Equal(t, "127.0.0.1:4321", otherCfg.Addr)
		assert.Empty(t, canceledCfg.Addr, "a canceled caller's connection config is left untouched")
		assert.Equal(t, int64(1), b.calls.Load())
		assert.Equal(t, int64(1), b.finished.Load(), "cancellation must not stop the shared discovery")
	})

	t.Run("every waiter cancels", func(t *testing.T) {
		b := newBlockedDiscovery()
		r := newProxyEndpointResolver(b.discover)
		r.arm()
		results := make(chan error, 2)
		for range 2 {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			go func() { results <- r.beforeConnect(ctx, mysql.NewConfig()) }()
		}
		for range 2 {
			require.ErrorIs(t, receiveWithin(t, results, 10*time.Second, "a caller"), context.DeadlineExceeded)
		}
		assert.Equal(t, int64(1), b.calls.Load())

		// The abandoned discovery still runs to completion, and the next
		// connection starts a discovery of its own.
		close(b.release)
		require.Eventually(t, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.inflight == nil
		}, 10*time.Second, time.Millisecond, "the abandoned discovery must finish")
		assert.Equal(t, int64(1), b.finished.Load())
		require.NoError(t, r.beforeConnect(context.Background(), mysql.NewConfig()))
		assert.Equal(t, int64(2), b.calls.Load())
		assert.Equal(t, int64(2), b.finished.Load())
	})

	t.Run("cancellation wins over a discovery that finished at the same time", func(t *testing.T) {
		// Both are ready when the canceled caller looks; without an explicit
		// check it would get the endpoint about half the time.
		for i := range 32 {
			b := newBlockedDiscovery()
			r := newProxyEndpointResolver(b.discover)
			r.arm()
			waiting := make(chan struct{}, 2)
			gate := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			canceledCfg := mysql.NewConfig()
			canceledDone, otherDone := make(chan error, 1), make(chan error, 1)
			go func() {
				canceledDone <- r.beforeConnect(&gatedDoneContext{Context: ctx, waiting: waiting, gate: gate}, canceledCfg)
			}()
			receiveWithin(t, waiting, 10*time.Second, "the canceled caller to wait")
			go func() {
				otherDone <- r.beforeConnect(&waitProbeContext{Context: context.Background(), waiting: waiting}, mysql.NewConfig())
			}()
			receiveWithin(t, waiting, 10*time.Second, "the other caller to wait")

			cancel()
			close(b.release)
			// The other caller returning means the discovery has published.
			require.NoError(t, receiveWithin(t, otherDone, 10*time.Second, "the other caller"))
			close(gate)
			err := receiveWithin(t, canceledDone, 10*time.Second, "the canceled caller")
			require.ErrorIs(t, err, context.Canceled, "iteration %d", i)
			require.Empty(t, canceledCfg.Addr, "iteration %d", i)
		}
	})
}

// errDialRecorded is what recordingDialer refuses every dial with.
var errDialRecorded = errors.New("test dial recorded")

// dialAttempt is one dial the driver made: the address and how much of its
// dial budget was left when it started.
type dialAttempt struct {
	addr   string
	budget time.Duration
}

// recordingDialer stands in for the network: it records every dial the driver
// makes and refuses it with errDialRecorded, so a test sees which endpoint a
// new connection would have used without a server behind it.
type recordingDialer struct {
	mu       sync.Mutex
	attempts []dialAttempt
}

func (d *recordingDialer) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	a := dialAttempt{addr: addr}
	if deadline, ok := ctx.Deadline(); ok {
		a.budget = time.Until(deadline)
	}
	d.mu.Lock()
	d.attempts = append(d.attempts, a)
	d.mu.Unlock()
	return nil, errDialRecorded
}

func (d *recordingDialer) dialed() []dialAttempt {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]dialAttempt(nil), d.attempts...)
}

// openResolverPool opens a pool the way openPool does for a provider, on a
// construction address of 127.0.0.1:4000 with r's hook installed, but dialing
// through d.
func openResolverPool(t *testing.T, r *proxyEndpointResolver, d *recordingDialer, timeout time.Duration) *sql.DB {
	t.Helper()
	cfg, err := mysql.ParseDSN(buildDSN(proxy.DataNetwork, proxy.Endpoint{Host: "127.0.0.1", Port: 4000}, "beads", "root", "", ""))
	require.NoError(t, err)
	cfg.Timeout = timeout
	cfg.DialFunc = d.dial
	require.NoError(t, cfg.Apply(mysql.BeforeConnect(r.beforeConnect)))
	connector, err := mysql.NewConnector(cfg)
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestProxyEndpointResolverErrors pins that a failed discovery fails the new
// connection with the discovery error, wrapped so callers can still match it,
// and never as driver.ErrBadConn or by dialing the construction address; a
// later discovery that succeeds lets a connection through again.
func TestProxyEndpointResolverErrors(t *testing.T) {
	mismatch := &proxy.ErrUpstreamMismatch{RootDir: "/workspace", Want: "upstream-a", Have: "upstream-b"}
	results := []error{mismatch, fmt.Errorf("authenticate proxy: %w", proxy.ErrUnverifiableProcess), nil}
	var calls atomic.Int64
	r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
		if err := results[calls.Add(1)-1]; err != nil {
			return proxy.Endpoint{}, err
		}
		return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
	})
	r.arm()
	d := &recordingDialer{}
	db := openResolverPool(t, r, d, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := db.Conn(ctx)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "uow: rediscover proxy endpoint: "), "got %q", err)
	var got *proxy.ErrUpstreamMismatch
	require.ErrorAs(t, err, &got)
	assert.Same(t, mismatch, got)
	assert.True(t, proxy.IsUpstreamMismatch(err))
	assert.NotErrorIs(t, err, driver.ErrBadConn)

	_, err = db.Conn(ctx)
	require.ErrorIs(t, err, proxy.ErrUnverifiableProcess)
	assert.True(t, strings.HasPrefix(err.Error(), "uow: rediscover proxy endpoint: "), "got %q", err)
	assert.NotErrorIs(t, err, driver.ErrBadConn)
	assert.Empty(t, d.dialed(), "a failed discovery must not fall back to the construction address")

	_, err = db.Conn(ctx)
	require.ErrorIs(t, err, errDialRecorded)
	dialed := d.dialed()
	require.Len(t, dialed, 1)
	assert.Equal(t, "127.0.0.1:4321", dialed[0].addr)
	assert.Equal(t, int64(3), calls.Load())
}

// TestProxyEndpointResolverDiscoveryBudget pins where the time goes: discovery
// runs before the driver starts its dial timeout, so a slow discovery leaves
// the dial its full budget, while the caller's own deadline still ends its
// wait promptly.
func TestProxyEndpointResolverDiscoveryBudget(t *testing.T) {
	const dialTimeout = 200 * time.Millisecond

	t.Run("dial gets a fresh budget after a slow discovery", func(t *testing.T) {
		r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
			time.Sleep(2 * dialTimeout)
			return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
		})
		r.arm()
		d := &recordingDialer{}
		db := openResolverPool(t, r, d, dialTimeout)

		_, err := db.Conn(context.Background())
		require.ErrorIs(t, err, errDialRecorded)
		dialed := d.dialed()
		require.Len(t, dialed, 1)
		assert.Equal(t, "127.0.0.1:4321", dialed[0].addr)
		assert.Greater(t, dialed[0].budget, dialTimeout/2, "discovery must not spend the dial timeout")
	})

	t.Run("caller deadline ends the wait", func(t *testing.T) {
		b := newBlockedDiscovery()
		t.Cleanup(func() { close(b.release) })
		r := newProxyEndpointResolver(b.discover)
		r.arm()
		d := &recordingDialer{}
		db := openResolverPool(t, r, d, 5*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := db.Conn(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 5*time.Second)
		assert.Empty(t, d.dialed())
	})
}

// TestProxyEndpointResolverErrorsShareFailedFlight pins that new connections
// overlapping one failed discovery all receive its wrapped cause: the
// discovery runs once and none of them dials the construction address.
// Adapted from the glm candidate's shared-failed-flight case.
func TestProxyEndpointResolverErrorsShareFailedFlight(t *testing.T) {
	const hooks = 2
	refused := errors.New("discovery refused")
	var calls atomic.Int64
	entered := make(chan struct{}, hooks)
	release := make(chan struct{})
	r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return proxy.Endpoint{}, refused
	})
	r.arm()
	d := &recordingDialer{}
	cfg, err := mysql.ParseDSN(buildDSN(proxy.DataNetwork, proxy.Endpoint{Host: "127.0.0.1", Port: 4000}, "beads", "root", "", ""))
	require.NoError(t, err)
	cfg.DialFunc = d.dial
	require.NoError(t, cfg.Apply(mysql.BeforeConnect(r.beforeConnect)))
	connector, err := mysql.NewConnector(cfg)
	require.NoError(t, err)

	waiting := make(chan struct{}, hooks)
	results := make(chan error, hooks)
	for range hooks {
		ctx := &waitProbeContext{Context: context.Background(), waiting: waiting}
		go func() {
			conn, err := connector.Connect(ctx)
			if conn != nil {
				_ = conn.Close()
			}
			results <- err
		}()
	}
	// Fail discovery only once both connections are inside the overlap.
	deadline := time.After(10 * time.Second)
	for waited, ran := 0, 0; waited < hooks && ran < hooks; {
		select {
		case <-waiting:
			waited++
		case <-entered:
			ran++
		case <-deadline:
			t.Fatalf("connections never all reached discovery: %d waiting, %d discovering", waited, ran)
		}
	}
	close(release)
	for i := range hooks {
		err := receiveWithin(t, results, 10*time.Second, "a connection")
		require.ErrorIs(t, err, refused, "connection %d", i)
		assert.True(t, strings.HasPrefix(err.Error(), "uow: rediscover proxy endpoint: "), "connection %d: got %q", i, err)
		assert.NotErrorIs(t, err, driver.ErrBadConn, "connection %d", i)
	}
	assert.Equal(t, int64(1), calls.Load(), "overlapping connections must share one failed discovery")
	assert.Empty(t, d.dialed(), "a failed discovery must not fall back to the construction address")
}

// TestProxyEndpointResolverClose pins that provider Close does not return while
// a discovery is in flight, so no discovery worker outlives the provider, and
// that afterwards every hook is refused without discovering or dialing.
func TestProxyEndpointResolverClose(t *testing.T) {
	b := newBlockedDiscovery()
	r := newProxyEndpointResolver(b.discover)
	r.arm()
	d := &recordingDialer{}
	p := &doltSQLProvider{db: openResolverPool(t, r, d, 5*time.Second), endpointResolver: r}
	// A pool the provider does not own, sharing its resolver, shows what a
	// connection attempt through the real driver does after Close.
	other := openResolverPool(t, r, d, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connDone := make(chan error, 1)
	go func() {
		conn, err := p.db.Conn(ctx)
		if conn != nil {
			_ = conn.Close()
		}
		connDone <- err
	}()
	receiveWithin(t, b.entered, 10*time.Second, "discovery to start")

	closed := make(chan error, 1)
	go func() { closed <- p.Close(ctx) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while discovery was still in flight", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(b.release)
	require.NoError(t, receiveWithin(t, closed, 10*time.Second, "Close"))
	assert.Equal(t, int64(1), b.finished.Load(), "the in-flight discovery must have finished when Close returns")
	r.mu.Lock()
	assert.Nil(t, r.inflight, "no discovery may remain in flight after Close")
	r.mu.Unlock()
	require.Error(t, receiveWithin(t, connDone, 10*time.Second, "the in-flight connection"))

	cfg := mysql.NewConfig()
	cfg.Addr = "127.0.0.1:4000"
	require.ErrorIs(t, r.beforeConnect(context.Background(), cfg), errProviderClosed)
	assert.Equal(t, "127.0.0.1:4000", cfg.Addr)
	_, err := other.Conn(ctx)
	require.ErrorIs(t, err, errProviderClosed)
	assert.Equal(t, int64(1), b.calls.Load(), "no discovery may start after Close")
	assert.Empty(t, d.dialed(), "nothing may be dialed after Close")
	require.NoError(t, p.Close(ctx), "Close is idempotent")
}
