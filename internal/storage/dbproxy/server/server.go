package server

import (
	"context"
	"net"
)

type DatabaseServer interface {
	ID(ctx context.Context) string
	DSN(ctx context.Context, database, user, password string) string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Running(ctx context.Context) bool
	Dial(ctx context.Context) (net.Conn, error)
}
