package proxy

import "sync"

type Counters struct {
	ListenAndServeCalls  int64
	BackendStartCalls    int64
	BackendStopCalls     int64
	IdleTimeouts         int64
	SignalsReceived      int64
	AcceptCalls          int64
	AcceptErrors         int64
	BackendDialAttempts  int64
	BackendDialSuccess   int64
	BackendDialErrors    int64
	HandledConns         int64
	BytesClientToBackend int64
	BytesBackendToClient int64
}

type Stats struct {
	mu       sync.Mutex
	counters Counters
}

func (s *Stats) Snapshot() Counters {
	if s == nil {
		return Counters{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counters
}

func (s *Stats) update(fn func(*Counters)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	fn(&s.counters)
	s.mu.Unlock()
}

func (s *Stats) IncListenAndServe()     { s.update(func(c *Counters) { c.ListenAndServeCalls++ }) }
func (s *Stats) IncBackendStart()       { s.update(func(c *Counters) { c.BackendStartCalls++ }) }
func (s *Stats) IncBackendStop()        { s.update(func(c *Counters) { c.BackendStopCalls++ }) }
func (s *Stats) IncIdleTimeout()        { s.update(func(c *Counters) { c.IdleTimeouts++ }) }
func (s *Stats) IncSignalReceived()     { s.update(func(c *Counters) { c.SignalsReceived++ }) }
func (s *Stats) IncAccept()             { s.update(func(c *Counters) { c.AcceptCalls++ }) }
func (s *Stats) IncAcceptError()        { s.update(func(c *Counters) { c.AcceptErrors++ }) }
func (s *Stats) IncBackendDialAttempt() { s.update(func(c *Counters) { c.BackendDialAttempts++ }) }
func (s *Stats) IncBackendDialSuccess() { s.update(func(c *Counters) { c.BackendDialSuccess++ }) }
func (s *Stats) IncBackendDialError()   { s.update(func(c *Counters) { c.BackendDialErrors++ }) }
func (s *Stats) IncHandledConn()        { s.update(func(c *Counters) { c.HandledConns++ }) }
func (s *Stats) AddBytesClientToBackend(n int64) {
	s.update(func(c *Counters) { c.BytesClientToBackend += n })
}
func (s *Stats) AddBytesBackendToClient(n int64) {
	s.update(func(c *Counters) { c.BytesBackendToClient += n })
}
