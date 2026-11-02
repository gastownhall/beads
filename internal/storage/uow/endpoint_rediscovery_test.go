package uow

import (
	"context"
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