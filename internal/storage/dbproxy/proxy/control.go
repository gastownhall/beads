package proxy

import (
	"bufio"
	"crypto/subtle"
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
	maxConcurrentIdentRequests = 8
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
	slots    chan struct{}
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
		slots:    make(chan struct{}, maxConcurrentIdentRequests),
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
			case <-time.After(delay):
			}
			if delay *= 2; delay > controlAcceptRetryMax {
				delay = controlAcceptRetryMax
			}
			continue
		}
		consecutiveErrors = 0
		delay = controlAcceptRetryDelay
		select {
		case s.slots <- struct{}{}:
			go func() {
				defer func() { <-s.slots }()
				s.handle(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (s *controlServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(identDeadline)); err != nil {
		return
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxIdentRequestBytes+1)).ReadString('\n')
	if err != nil || len(line) > maxIdentRequestBytes || !strings.HasSuffix(line, "\n") {
		return
	}
	parts := strings.Split(strings.TrimSuffix(line, "\n"), " ")
	if len(parts) != 3 || parts[0] != "IDENT" {
		return
	}
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.secret)) != 1 {
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
