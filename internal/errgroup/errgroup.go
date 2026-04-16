// Package errgroup provides a lightweight replacement for golang.org/x/sync/errgroup.
package errgroup

import (
	"context"
	"sync"
)

// Group collects goroutines and returns the first non-nil error.
type Group struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
	sem    chan struct{}
}

// WithContext returns a new Group derived from ctx. The derived context is
// cancelled the first time a goroutine returns a non-nil error.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

// SetLimit limits the number of active goroutines. A negative value
// means no limit.
func (g *Group) SetLimit(n int) {
	if n > 0 {
		g.sem = make(chan struct{}, n)
	}
}

// Go starts fn in a new goroutine. The first non-nil error is recorded
// and the derived context (if any) is cancelled.
func (g *Group) Go(fn func() error) {
	if g.sem != nil {
		g.sem <- struct{}{}
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if g.sem != nil {
			defer func() { <-g.sem }()
		}
		if err := fn(); err != nil {
			g.once.Do(func() {
				g.err = err
				if g.cancel != nil {
					g.cancel()
				}
			})
		}
	}()
}

// Wait blocks until all goroutines have finished and returns the first
// non-nil error (if any).
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel()
	}
	return g.err
}
