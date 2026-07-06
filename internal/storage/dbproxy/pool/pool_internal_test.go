package pool

import (
	"context"
	"os"
	"testing"
	"time"
)

func internalDoltAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("BEADS_POOL_TEST_DOLT_ADDR")
	if addr == "" {
		t.Skip("BEADS_POOL_TEST_DOLT_ADDR not set; skipping live-Dolt pooler test")
	}
	return addr
}

// TestBorrowTimeout verifies that when all backends are checked out, an
// additional Borrow blocks for ~BorrowWait and then returns ErrBorrowTimeout
// rather than hanging forever; and that releasing a backend unblocks a waiter.
func TestBorrowTimeout(t *testing.T) {
	addr := internalDoltAddr(t)
	p, err := New(context.Background(), Config{
		Addr:       addr,
		Size:       MinSize, // 4
		BorrowWait: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	borrowed := make([]*backend, 0, MinSize)
	for i := 0; i < MinSize; i++ {
		b, err := p.Borrow(context.Background())
		if err != nil {
			t.Fatalf("borrow %d: %v", i, err)
		}
		borrowed = append(borrowed, b)
	}

	start := time.Now()
	_, err = p.Borrow(context.Background())
	elapsed := time.Since(start)
	if err != ErrBorrowTimeout {
		t.Fatalf("expected ErrBorrowTimeout, got %v", err)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("borrow returned too fast (%s); should have waited ~300ms", elapsed)
	}

	// Release one and confirm a subsequent borrow now succeeds quickly.
	p.Release(borrowed[0])
	got := make(chan error, 1)
	go func() {
		b, err := p.Borrow(context.Background())
		if err == nil {
			p.Release(b)
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("borrow after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("borrow after release did not unblock")
	}

	for _, b := range borrowed[1:] {
		p.Release(b)
	}
}

// TestDiscardKeepsPoolSize verifies that discarding a backend (simulating an
// error path) asynchronously reconnects a replacement so the pool's borrowable
// count returns to its configured size.
func TestDiscardKeepsPoolSize(t *testing.T) {
	addr := internalDoltAddr(t)
	p, err := New(context.Background(), Config{Addr: addr, Size: MinSize})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	b, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	p.Discard(b) // throws away + async reconnect

	// Eventually all MinSize backends should be borrowable again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		held := make([]*backend, 0, MinSize)
		ok := true
		for i := 0; i < MinSize; i++ {
			bb, err := p.Borrow(context.Background())
			if err != nil {
				ok = false
				break
			}
			held = append(held, bb)
		}
		for _, hb := range held {
			p.Release(hb)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool did not recover to size %d after discard", MinSize)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
