package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

type ExternalDoltServer struct {
	id              string
	host            string
	port            int
	socket          string
	keepAlivePeriod time.Duration

	started atomic.Bool
}

var _ DatabaseServer = (*ExternalDoltServer)(nil)

func NewExternalDoltServer(cfg configfile.ExternalDoltConfig) (*ExternalDoltServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	keepAlive := cfg.KeepAlivePeriod
	if keepAlive == 0 {
		keepAlive = defaultKeepAlivePeriod
	}
	return &ExternalDoltServer{
		id:              ExternalDoltServerID(cfg),
		host:            cfg.Host,
		port:            cfg.Port,
		socket:          cfg.Socket,
		keepAlivePeriod: keepAlive,
	}, nil
}

func ExternalDoltServerID(cfg configfile.ExternalDoltConfig) string {
	sum := sha256.Sum256([]byte(externalDoltServerTarget(cfg)))
	return hex.EncodeToString(sum[:])
}

func externalDoltServerTarget(cfg configfile.ExternalDoltConfig) string {
	if cfg.Socket != "" {
		return "unix://" + cfg.Socket
	}
	return "tcp://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

func (s *ExternalDoltServer) ID(_ context.Context) string {
	return s.id
}

func (s *ExternalDoltServer) DSN(_ context.Context, database, user, password string) string {
	dsn := util.DoltServerDSN{
		User:     user,
		Password: password,
		Database: database,
	}
	if s.socket != "" {
		dsn.Socket = s.socket
	} else {
		dsn.Host = s.host
		dsn.Port = s.port
	}
	return dsn.String()
}

func (s *ExternalDoltServer) Start(_ context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("server: ExternalDoltServer.Start: server already started")
	}
	return nil
}

func (s *ExternalDoltServer) Stop(_ context.Context) error {
	s.started.Store(false)
	return nil
}

func (s *ExternalDoltServer) Running(_ context.Context) bool {
	return s.started.Load()
}

func (s *ExternalDoltServer) Dial(ctx context.Context) (net.Conn, error) {
	network, addr := "tcp", net.JoinHostPort(s.host, strconv.Itoa(s.port))
	if s.socket != "" {
		network, addr = "unix", s.socket
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("server: ExternalDoltServer.Dial: %w", err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(s.keepAlivePeriod)
	}
	return conn, nil
}
