package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
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

// identConn is one admitted control connection tracked in a controlServer's
// active list. The list is ordered oldest accepted first; membership (the
// active flag) is guarded by controlServer.mu.
type identConn struct {
	conn net.Conn
	// active reports whether the entry is still in the server's list. It
	// makes removal idempotent, so eviction, shutdown drain and a handler's
	// own cleanup can all race for the same entry harmlessly.
	active bool
}

type controlServer struct {
	listener net.Listener
	secret   string
	done     chan struct{}
	closing  chan struct{}
	once     sync.Once
	reply    func() identity.IdentReply

	// mu guards conns, closed and every entry's active flag. Never hold it
	// across socket I/O, callbacks or waits.
	mu     sync.Mutex
	conns  []*identConn // tracked connections, oldest accepted first
	closed bool         // admission refused once Close started draining
	wg     sync.WaitGroup // admitted handlers, drained by Close
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
		// The accept loop has exited, so admission can no longer add
		// entries; drain the ones it left behind and wait for their
		// handlers to unwind.
		<-s.done
		s.drain()
	})
	return err
}

// drain detaches and closes every tracked connection — unblocking in-flight
// reads and reply writes — and waits for the handlers to unwind. Handler
// cleanup runs through the same idempotent release path. Every handler is
// bounded by its admission deadline, so the wait cannot outlive identDeadline
// past the last admission.
func (s *controlServer) drain() {
	s.mu.Lock()
	s.closed = true
	entries := s.conns
	s.conns = nil
	for _, entry := range entries {
		entry.active = false
	}
	s.mu.Unlock()
	for _, entry := range entries {
		_ = entry.conn.Close()
	}
	s.wg.Wait()
}

// activeIdentConns reports how many control connections are currently
// tracked; used by tests to observe admission, eviction and cleanup.
func (s *controlServer) activeIdentConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
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
		s.admit(conn)
	}
}

// admit tracks conn as the newest active control connection and launches its
// handler, returning the tracking entry. The control listener is loopback
// TCP, which any local uid can reach, so admission must never let a pool of
// parked idle peers exclude a fresh probe: at capacity the oldest tracked
// entry is evicted — detached here and closed outside the mutex — and its
// handler unwinds through the same idempotent release path as any other
// termination. The admission deadline is set here, on the accept goroutine,
// so a delayed handler launch cannot extend an idle socket's lifetime.
func (s *controlServer) admit(conn net.Conn) *identConn {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return nil
	}
	var victim *identConn
	if len(s.conns) >= maxConcurrentIdentRequests {
		victim = s.conns[0]
		s.conns = s.conns[1:]
		victim.active = false
	}
	entry := &identConn{conn: conn, active: true}
	s.conns = append(s.conns, entry)
	s.mu.Unlock()

	if victim != nil {
		// Closing the victim unblocks its handler's read or reply write;
		// closing it here also prevents any later successful response on
		// it. Its own release is a harmless no-op afterwards.
		_ = victim.conn.Close()
	}
	if err := conn.SetDeadline(time.Now().Add(identDeadline)); err != nil {
		// A connection whose deadline cannot be set is unusable: close it
		// and drop its entry so it does not keep capacity occupied.
		s.release(entry)
		return nil
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handle(entry)
	}()
	return entry
}

// release detaches entry from the tracked list and closes its connection.
// Both are idempotent, so eviction, shutdown drain and normal handler
// cleanup can all run for the same entry, and a released entry can never
// disturb another connection's membership.
func (s *controlServer) release(entry *identConn) {
	s.mu.Lock()
	entry.active = false
	if i := slices.Index(s.conns, entry); i >= 0 {
		s.conns = append(s.conns[:i], s.conns[i+1:]...)
	}
	s.mu.Unlock()
	_ = entry.conn.Close()
}

// handle serves one admitted connection to termination — reply writes
// included — so the entry stays tracked for the connection's whole lifetime
// instead of only until its first read completes.
func (s *controlServer) handle(entry *identConn) {
	defer s.release(entry)
	conn := entry.conn
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
