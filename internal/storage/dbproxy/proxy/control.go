package proxy

import (
	"bufio"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
)

const (
	maxIdentRequestBytes       = 256
	identDeadline              = 2 * time.Second
	maxConcurrentIdentRequests = 256
	controlAcceptRetryDelay    = 10 * time.Millisecond
	controlAcceptRetryMax      = 5 * time.Second
)

type controlServer struct {
	listener net.Listener
	secret   string
	done     chan struct{}
	closing  chan struct{}
	once     sync.Once
	reply    func() identity.IdentReply

	// mu guards active: the connections admitted and not yet released,
	// oldest accepted first. The zero value is an empty pool.
	mu     sync.Mutex
	active list.List // of *controlConn
}

// controlConn is one admitted control connection. elem is its place in
// controlServer.active, nil once it left the pool (released or evicted); it
// is guarded by controlServer.mu.
type controlConn struct {
	conn net.Conn
	elem *list.Element
}

func startControl(rootDir string, reply func() identity.IdentReply) (*controlServer, error) {
	secret, err := identity.ReadSecret(rootDir)
	if err != nil {
		return nil, fmt.Errorf("proxy: read control secret: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("proxy: listen control: %w", err)
	}
	s := &controlServer{
		listener: ln,
		secret:   secret,
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
		reply:    reply,
	}
	go s.acceptLoop()
	return s, nil
}

func (s *controlServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func (s *controlServer) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closing)
		err = s.listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		<-s.done
	})
	return err
}

// acceptLoop retries transient Accept failures (e.g. EMFILE) indefinitely
// with capped exponential backoff instead of tearing anything down: the data
// path may still be perfectly healthy, and a proxy that stops answering
// identity probes merely degrades adoption to the caller's poll/retry path.
func (s *controlServer) acceptLoop() {
	s.acceptLoopWithAfter(time.After)
}

func (s *controlServer) acceptLoopWithAfter(after func(time.Duration) <-chan time.Time) {
	defer close(s.done)
	consecutiveErrors := 0
	delay := controlAcceptRetryDelay
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			consecutiveErrors++
			log.Printf(
				"dbproxy: control accept failed %d consecutive time(s), retrying in %s: %v",
				consecutiveErrors, delay, err,
			)
			select {
			case <-s.closing:
				return
			case <-after(delay):
			}
			if delay *= 2; delay > controlAcceptRetryMax {
				delay = controlAcceptRetryMax
			}
			continue
		}
		consecutiveErrors = 0
		delay = controlAcceptRetryDelay
		c := s.admit(conn)
		deadline := time.Now().Add(identDeadline)
		go s.serve(c, deadline)
	}
}

// admit adds conn to the pool as its newest member. The listener is reachable
// by any local user, so a full pool evicts its oldest connection rather than
// turning the newcomer away: idle connections that never send a request can
// delay a secret holder's probe but not lock it out.
func (s *controlServer) admit(conn net.Conn) *controlConn {
	c := &controlConn{conn: conn}
	var victim *controlConn
	s.mu.Lock()
	if s.active.Len() >= maxConcurrentIdentRequests {
		victim = s.active.Remove(s.active.Front()).(*controlConn)
		victim.elem = nil
	}
	c.elem = s.active.PushBack(c)
	s.mu.Unlock()
	if victim != nil {
		// Unblocks the victim's handler, which then releases nothing.
		_ = victim.conn.Close()
	}
	return c
}

// release closes c and removes it from the pool unless an eviction already
// did. It is safe to call more than once.
func (s *controlServer) release(c *controlConn) {
	s.mu.Lock()
	if c.elem != nil {
		s.active.Remove(c.elem)
		c.elem = nil
	}
	s.mu.Unlock()
	_ = c.conn.Close()
}

// serve answers one admitted connection. It keeps its place in the pool
// until it returns, reply write included, so the pool bounds every
// connection a handler still holds.
func (s *controlServer) serve(c *controlConn, deadline time.Time) {
	defer s.release(c)
	if err := c.conn.SetDeadline(deadline); err != nil {
		return
	}
	s.handle(c.conn)
}

func (s *controlServer) handle(conn net.Conn) {
	line, err := bufio.NewReader(io.LimitReader(conn, maxIdentRequestBytes+1)).ReadString('\n')
	if err != nil || len(line) > maxIdentRequestBytes || !strings.HasSuffix(line, "\n") {
		return
	}
	parts := strings.Split(strings.TrimSuffix(line, "\n"), " ")
	if len(parts) != 3 || parts[0] != "IDENT" {
		return
	}
	if !identity.VerifyIdentRequest(s.secret, parts[2], parts[1]) {
		return
	}
	reply := s.reply()
	reply.ControlPort = s.Port()
	signed, err := identity.SignIdentReply(reply, s.secret, parts[2])
	if err != nil {
		return
	}
	_ = writeIdentReply(conn, signed)
}

func writeIdentReply(w io.Writer, reply identity.IdentReply) error {
	return json.NewEncoder(w).Encode(reply)
}
