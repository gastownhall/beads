package uow

import (
	"context"
	"crypto/tls"
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
