package uow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// newScriptedResolver arms a resolver whose discovery is the injected closure,
// the instance-injected seam the resolver tests use instead of package
// globals.
func newScriptedResolver(resolve func(context.Context) (proxy.Endpoint, error)) *proxyEndpointResolver {
	r := &proxyEndpointResolver{resolve: resolve}
	r.arm()
	return r
}

// TestProxyEndpointResolverCoalesces pins #7295's overlap rule: several new
// connections that need the endpoint while ONE discovery is running share that
// discovery's result, and only later connections — after the overlap — start a
// fresh one. Overlap coalesces; nothing is cached.
//
// The scripted discovery makes overlap observable: its first call blocks until
// the test releases it, and any call that is NOT coalesced into that first one
// answers immediately with a different endpoint, so a waiter that ran its own
// discovery dials the wrong address and the count says how many ran.
func TestProxyEndpointResolverCoalesces(t *testing.T) {
	released := make(chan struct{})
	var calls atomic.Int32
	const (
		sharedPort  = 42424 // the one blocked discovery every waiter must join
		freshPort   = 42425 // what an uncoalesced later discovery returns
		concurrency = 3
	)
	shared := proxy.Endpoint{Host: "127.0.0.1", Port: sharedPort}
	fresh := proxy.Endpoint{Host: "127.0.0.1", Port: freshPort}
	r := newScriptedResolver(func(ctx context.Context) (proxy.Endpoint, error) {
		switch calls.Add(1) {
		case 1:
			select {
			case <-released:
				return shared, nil
			case <-ctx.Done():
				return proxy.Endpoint{}, ctx.Err()
			}
		default:
			return fresh, nil
		}
	})

	addrs := make([]string, concurrency)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cfg := mysql.NewConfig()
			cfg.Addr = "127.0.0.1:1"
			if err := r.beforeConnect(context.Background(), cfg); err != nil {
				t.Errorf("beforeConnect() = %v, want nil", err)
				return
			}
			addrs[i] = cfg.Addr
		}(i)
	}
	close(start)

	// Let the overlap form: one discovery running, every other waiter joined
	// to it. The generous window is for scheduling only — correctness below
	// does not depend on tighter timing.
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("discovery never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Second)
	close(released)
	wg.Wait()

	for i, addr := range addrs {
		if addr != shared.Address() {
			t.Errorf("waiter %d dialed %q, want the shared discovery's %q", i, addr, shared.Address())
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times for one overlap, want exactly 1", n)
	}

	// Nothing is cached for later connections: the next one discovers again.
	cfg := mysql.NewConfig()
	if err := r.beforeConnect(context.Background(), cfg); err != nil {
		t.Fatalf("next beforeConnect() = %v, want nil", err)
	}
	if addr := cfg.Addr; addr != fresh.Address() {
		t.Fatalf("the next connection dialed %q, want a fresh discovery's %q", addr, fresh.Address())
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery ran %d times total, want a fresh one after the overlap (2)", n)
	}
}

// TestProxyEndpointResolverCancellation pins #7295's cancellation rule: a
// caller's cancellation ends THAT caller's wait, promptly, and nothing else —
// the shared discovery keeps running (a spawned proxy child may already be
// adopted by a peer), the other waiters still get its result, and an
// already-canceled caller is refused before it can start work.
func TestProxyEndpointResolverCancellation(t *testing.T) {
	released := make(chan struct{})
	var calls atomic.Int32
	var completed atomic.Bool
	shared := proxy.Endpoint{Host: "127.0.0.1", Port: 42434}
	r := newScriptedResolver(func(ctx context.Context) (proxy.Endpoint, error) {
		calls.Add(1)
		select {
		case <-released:
			completed.Store(true)
			return shared, nil
		case <-ctx.Done():
			return proxy.Endpoint{}, ctx.Err()
		}
	})

	// The worker: admitted first, runs the blocked discovery.
	workerDone := make(chan error, 1)
	go func() {
		cfg := mysql.NewConfig()
		workerDone <- r.beforeConnect(context.Background(), cfg)
	}()
	waitForDiscoveryEntered(t, &calls, 1)

	// Two waiters join the pending flight; one of them cancels.
	canceledDone := make(chan error, 1)
	canceledCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		cfg := mysql.NewConfig()
		canceledDone <- r.beforeConnect(canceledCtx, cfg)
	}()
	peerDone := make(chan error, 1)
	go func() {
		cfg := mysql.NewConfig()
		peerDone <- r.beforeConnect(context.Background(), cfg)
	}()
	// Give both waiters a generous window to join the flight before cancelling.
	time.Sleep(500 * time.Millisecond)

	cancel()
	select {
	case err := <-canceledDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled waiter got %v, want its context error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter was not released while its discovery was still pending")
	}
	if calls.Load() != 1 {
		t.Fatalf("discovery was entered %d times, want the one still running", calls.Load())
	}
	if completed.Load() {
		t.Fatal("discovery completed before its release; cancellation must not run it down")
	}

	// Release: the surviving waiter and the worker both succeed from the one
	// shared discovery, which cancellation did not multiply.
	close(released)
	if err := <-peerDone; err != nil {
		t.Fatalf("surviving waiter: beforeConnect() = %v, want nil", err)
	}
	if err := <-workerDone; err != nil {
		t.Fatalf("worker: beforeConnect() = %v, want nil", err)
	}
	if !completed.Load() {
		t.Fatal("discovery never completed after release")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cancellation started %d extra discoveries, want none", n-1)
	}

	// An already-canceled caller is refused before it can start work: no new
	// discovery is entered on its behalf, and it does not wait on anything.
	canceledCalls := calls.Load()
	canceledAgain, cancelAgain := context.WithCancel(context.Background())
	cancelAgain()
	cfg := mysql.NewConfig()
	err := r.beforeConnect(canceledAgain, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("beforeConnect() on a canceled context = %v, want the context error", err)
	}
	if cfg.Addr != "" {
		t.Errorf("a canceled caller still resolved the endpoint %q", cfg.Addr)
	}
	if n := calls.Load(); n != canceledCalls {
		t.Fatalf("a canceled caller started discovery anyway (%d entries, want %d)", n, canceledCalls)
	}
}

// waitForDiscoveryEntered polls calls until want discoveries have been
// entered, failing the test if they never are.
func waitForDiscoveryEntered(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("discovery never entered call %d", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
