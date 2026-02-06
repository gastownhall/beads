//go:build cgo
package factory

import (
	"errors"
	"testing"
)

func TestIsServerConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp 127.0.0.1:3307: connect: connection refused"),
			want: true,
		},
		{
			name: "wrapped connection refused",
			err:  errors.New("failed to connect to Dolt server at 127.0.0.1:3307: dial tcp 127.0.0.1:3307: connect: connection refused"),
			want: true,
		},
		{
			name: "no such host",
			err:  errors.New("dial tcp: lookup badhost.invalid: no such host"),
			want: true,
		},
		{
			name: "i/o timeout",
			err:  errors.New("dial tcp 10.0.0.1:3307: i/o timeout"),
			want: true,
		},
		{
			name: "connection reset",
			err:  errors.New("read tcp 127.0.0.1:54321->127.0.0.1:3307: connection reset by peer"),
			want: true,
		},
		{
			name: "network unreachable",
			err:  errors.New("dial tcp 192.168.1.1:3307: connect: network is unreachable"),
			want: true,
		},
		{
			name: "auth error is not connection error",
			err:  errors.New("Error 1045: Access denied for user 'root'@'localhost'"),
			want: false,
		},
		{
			name: "schema error is not connection error",
			err:  errors.New("failed to create schema: table already exists"),
			want: false,
		},
		{
			name: "database error is not connection error",
			err:  errors.New("failed to create database: unknown error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isServerConnectionError(tt.err)
			if got != tt.want {
				t.Errorf("isServerConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}


