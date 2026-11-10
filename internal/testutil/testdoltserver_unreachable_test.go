//go:build !windows

package testutil

import (
	"errors"
	"fmt"
	"testing"
)

func TestServerUnreachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "refused", err: errors.New("dial tcp 127.0.0.1:32772: connect: connection refused"), want: true},
		{name: "unreachable", err: fmt.Errorf("Dolt server unreachable at 127.0.0.1:1: %w", errors.New("connection refused")), want: true},
		{name: "eof", err: errors.New("unexpected EOF"), want: true},
		{name: "query", err: errors.New("Error 1105: query aborted"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ServerUnreachable(tc.err); got != tc.want {
				t.Fatalf("ServerUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
