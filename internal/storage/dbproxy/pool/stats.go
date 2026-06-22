package pool

import "sync"

// Counters is an immutable snapshot of pool metrics. All fields are cumulative
// since pool creation.
type Counters struct {
	// Reconnects counts backends re-dialed due to a failed liveness/keepalive
	// ping (idle reaping recovery).
	Reconnects int64
	// Discards counts backends thrown away after an error and replaced (never
	// reset-reused).
	Discards int64
	// BorrowWaits counts borrows that had to block because all backends were
	// checked out (a sign the pool may be undersized for the load).
	BorrowWaits int64
	// BorrowTimeouts counts borrows that gave up after BorrowWait elapsed.
	BorrowTimeouts int64
	// ClientConns counts client connections accepted by the handler.
	ClientConns int64
	// PreparesRejected counts COM_STMT_PREPARE requests that could not be
	// served (should be 0 in normal operation).
	PreparesRejected int64
}

// Stats is a concurrency-safe counter bag for a Pool. A nil *Stats is valid and
// makes every Inc a no-op, so production callers may leave it nil.
type Stats struct {
	mu sync.Mutex
	c  Counters
}

// Snapshot returns a copy of the current counters. Safe on a nil receiver.
func (s *Stats) Snapshot() Counters {
	if s == nil {
		return Counters{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c
}

func (s *Stats) update(fn func(*Counters)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	fn(&s.c)
	s.mu.Unlock()
}

func (s *Stats) IncReconnect()       { s.update(func(c *Counters) { c.Reconnects++ }) }
func (s *Stats) IncDiscard()         { s.update(func(c *Counters) { c.Discards++ }) }
func (s *Stats) IncBorrowWait()      { s.update(func(c *Counters) { c.BorrowWaits++ }) }
func (s *Stats) IncBorrowTimeout()   { s.update(func(c *Counters) { c.BorrowTimeouts++ }) }
func (s *Stats) IncClientConn()      { s.update(func(c *Counters) { c.ClientConns++ }) }
func (s *Stats) IncPrepareRejected() { s.update(func(c *Counters) { c.PreparesRejected++ }) }
