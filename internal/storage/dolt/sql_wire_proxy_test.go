package dolt

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// comQuery is the MySQL command byte of a text-protocol statement. Every
// statement a store sends travels as one: its DSN interpolates parameters.
const comQuery = 0x03

// sqlWireProxy forwards MySQL wire traffic between stores under test and the
// test Dolt server. It records every statement it forwards and can trap the
// next one that matches a predicate, either holding it until the test releases
// it (to order two writers' commits deterministically) or forwarding it and
// cutting the client off before the server's answer arrives (a response lost
// after the server acted on the statement). Production code needs no test hook.
type sqlWireProxy struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	trap    *sqlWireTrap
	held    []*sqlWireTrap
	queries []string
	conns   []net.Conn
}

type sqlWireTrap struct {
	match    func(query string) bool
	drop     bool
	sprung   chan struct{}
	release  chan struct{}
	released sync.Once
}

func (trap *sqlWireTrap) open() { trap.released.Do(func() { close(trap.release) }) }

func startSQLWireProxy(t *testing.T) *sqlWireProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &sqlWireProxy{ln: ln, target: fmt.Sprintf("127.0.0.1:%d", testServerPort)}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(client)
		}
	}()
	t.Cleanup(p.close)
	return p
}

func (p *sqlWireProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

// holdNext traps the next statement matching match and keeps it from the
// server until release is called. sprung is closed once it is caught.
func (p *sqlWireProxy) holdNext(match func(query string) bool) (sprung <-chan struct{}, release func()) {
	trap := p.arm(match, false)
	return trap.sprung, trap.open
}

// dropResponseToNext traps the next statement matching match: the server runs
// it, and the client's connection is cut instead of receiving the answer.
func (p *sqlWireProxy) dropResponseToNext(match func(query string) bool) (sprung <-chan struct{}) {
	return p.arm(match, true).sprung
}

func (p *sqlWireProxy) arm(match func(query string) bool, drop bool) *sqlWireTrap {
	trap := &sqlWireTrap{match: match, drop: drop, sprung: make(chan struct{}), release: make(chan struct{})}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trap != nil {
		p.trap.open()
	}
	p.trap = trap
	return trap
}

// mark returns a position in the statement log for statementsSince.
func (p *sqlWireProxy) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queries)
}

// statementsSince counts the statements forwarded after mark that match.
func (p *sqlWireProxy) statementsSince(mark int, match func(query string) bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, q := range p.queries[mark:] {
		if match(q) {
			n++
		}
	}
	return n
}

// intercept logs query and hands back the armed trap when it catches query.
func (p *sqlWireProxy) intercept(query string) *sqlWireTrap {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, query)
	trap := p.trap
	if trap == nil || !trap.match(query) {
		return nil
	}
	p.trap = nil
	if !trap.drop {
		p.held = append(p.held, trap)
	}
	close(trap.sprung)
	return trap
}

func (p *sqlWireProxy) close() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trap != nil {
		p.trap.open()
	}
	for _, trap := range p.held {
		trap.open()
	}
	for _, c := range p.conns {
		_ = c.Close()
	}
}

func (p *sqlWireProxy) handle(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	p.mu.Lock()
	p.conns = append(p.conns, client, server)
	p.mu.Unlock()
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = server.Close() }) }
	var dropping atomic.Bool
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if dropping.Load() {
					closeBoth()
					return
				}
				if _, werr := client.Write(buf[:n]); werr != nil {
					closeBoth()
					return
				}
			}
			if err != nil {
				closeBoth()
				return
			}
		}
	}()
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(client, hdr); err != nil {
			closeBoth()
			return
		}
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		packet := make([]byte, 4+n)
		copy(packet, hdr)
		if _, err := io.ReadFull(client, packet[4:]); err != nil {
			closeBoth()
			return
		}
		// A command starts a new exchange (sequence id 0); the handshake's
		// packets do not.
		if hdr[3] == 0 && n > 1 && packet[4] == comQuery {
			if trap := p.intercept(strings.TrimSpace(string(packet[5:]))); trap != nil {
				if trap.drop {
					dropping.Store(true)
				} else {
					<-trap.release
				}
			}
		}
		if _, err := server.Write(packet); err != nil {
			closeBoth()
			return
		}
	}
}

func isCommitStatement(query string) bool { return strings.EqualFold(query, "COMMIT") }
