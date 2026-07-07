package server

import (
	"context"
	"io"
	"net"
	"sync"
)

type TestDatabaseServer interface {
	DatabaseServer
	Snapshot() Counters
}

type Counters struct {
	IDCalls       int64
	DSNCalls      int64
	StartCalls    int64
	StopCalls     int64
	RunningCalls  int64
	DialCalls     int64
	DialErrors    int64
	AcceptedConns int64 // cumulative
	OpenConns     int64 // current (not cumulative)
	BytesIn       int64 // bytes the handler read from the proxy side
	BytesOut      int64 // bytes the handler wrote back
}

// TestDatabaseServerImpl is the in-memory pipe-based implementation of
// TestDatabaseServer. Behavior knobs (StartErr, DialErr, Handler, ...) are
// exported fields; tests set them directly on the concrete type before
// exercising the proxy. They should not be mutated while the proxy is
// running.
type TestDatabaseServerImpl struct {
	ID_      string                 // returned by ID(); default "test-server"
	DSN_     string                 // returned by DSN(); default "test://in-memory"
	StartErr error                  // if non-nil, Start returns it (and does not mark started)
	StopErr  error                  // if non-nil, Stop returns it after closing conns
	DialErr  error                  // if non-nil, Dial returns it
	Handler  func(backend net.Conn) // runs once per Dial; default EchoHandler

	mu        sync.Mutex
	counters  Counters
	started   bool
	openConns []net.Conn
}

var _ TestDatabaseServer = (*TestDatabaseServerImpl)(nil)

func New() *TestDatabaseServerImpl {
	return &TestDatabaseServerImpl{
		ID_:     "test-server",
		DSN_:    "test://in-memory",
		Handler: EchoHandler,
	}
}

// EchoHandler reads bytes from c and writes them back. Default Dial handler.
func EchoHandler(c net.Conn) {
	defer func() { _ = c.Close() }()
	_, _ = io.Copy(c, c)
}

// DiscardHandler reads bytes from c and throws them away. Useful when tests
// only care that the proxy attempted Dial / forwarded one direction.
func DiscardHandler(c net.Conn) {
	defer func() { _ = c.Close() }()
	_, _ = io.Copy(io.Discard, c)
}

// SetDialErr atomically replaces DialErr. Use to flip Dial behavior after
// the proxy has already reached readiness.
func (s *TestDatabaseServerImpl) SetDialErr(err error) {
	s.mu.Lock()
	s.DialErr = err
	s.mu.Unlock()
}

func (s *TestDatabaseServerImpl) Snapshot() Counters {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counters
}

func (s *TestDatabaseServerImpl) ID(_ context.Context) string {
	s.mu.Lock()
	s.counters.IDCalls++
	s.mu.Unlock()
	return s.ID_
}

func (s *TestDatabaseServerImpl) DSN(_ context.Context, _, _, _ string) string {
	s.mu.Lock()
	s.counters.DSNCalls++
	s.mu.Unlock()
	return s.DSN_
}

func (s *TestDatabaseServerImpl) Start(_ context.Context) error {
	s.mu.Lock()
	s.counters.StartCalls++
	if s.StartErr == nil {
		s.started = true
	}
	err := s.StartErr
	s.mu.Unlock()
	return err
}

func (s *TestDatabaseServerImpl) Stop(_ context.Context) error {
	s.mu.Lock()
	s.counters.StopCalls++
	s.started = false
	conns := s.openConns
	s.openConns = nil
	err := s.StopErr
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return err
}

func (s *TestDatabaseServerImpl) Running(_ context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters.RunningCalls++
	return s.started
}

func (s *TestDatabaseServerImpl) Dial(_ context.Context) (net.Conn, error) {
	s.mu.Lock()
	s.counters.DialCalls++
	if s.DialErr != nil {
		s.counters.DialErrors++
		err := s.DialErr
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()

	proxySide, backendSide := net.Pipe()
	wrapped := &countingConn{Conn: backendSide, s: s}

	s.mu.Lock()
	s.openConns = append(s.openConns, wrapped)
	s.counters.AcceptedConns++
	s.counters.OpenConns++
	s.mu.Unlock()

	go func() {
		defer s.untrackConn(wrapped)
		s.Handler(wrapped)
	}()

	return proxySide, nil
}

func (s *TestDatabaseServerImpl) untrackConn(c net.Conn) {
	s.mu.Lock()
	for i, oc := range s.openConns {
		if oc == c {
			s.openConns = append(s.openConns[:i], s.openConns[i+1:]...)
			break
		}
	}
	s.counters.OpenConns--
	s.mu.Unlock()
}

type countingConn struct {
	net.Conn
	s *TestDatabaseServerImpl
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.s.mu.Lock()
		c.s.counters.BytesIn += int64(n)
		c.s.mu.Unlock()
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.s.mu.Lock()
		c.s.counters.BytesOut += int64(n)
		c.s.mu.Unlock()
	}
	return n, err
}
