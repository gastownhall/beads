package dolt

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestServerConnectionNeedsPreflight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "remote IPv4 writable",
			cfg:  Config{ServerHost: "100.122.24.50", ServerPort: 3309, ReadOnly: false},
			want: false,
		},
		{
			name: "remote hostname read-only",
			cfg:  Config{ServerHost: "dolt.tailnet.example", ServerPort: 3309, ReadOnly: true},
			want: false,
		},
		{
			name: "loopback IPv4",
			cfg:  Config{ServerHost: "127.0.0.1", ServerPort: 3307},
			want: true,
		},
		{
			name: "localhost hostname",
			cfg:  Config{ServerHost: "LOCALHOST", ServerPort: 3307},
			want: true,
		},
		{
			name: "loopback IPv6",
			cfg:  Config{ServerHost: "::1", ServerPort: 3307},
			want: true,
		},
		{
			name: "all interfaces",
			cfg:  Config{ServerHost: "0.0.0.0", ServerPort: 3307},
			want: true,
		},
		{
			name: "unix socket on remote-configured host",
			cfg: Config{
				ServerHost:   "100.122.24.50",
				ServerPort:   3309,
				ServerSocket: "/tmp/dolt.sock",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := serverConnectionNeedsPreflight(&tt.cfg); got != tt.want {
				t.Fatalf("serverConnectionNeedsPreflight(%+v) = %t, want %t", tt.cfg, got, tt.want)
			}
		})
	}
}

func TestDialServerPreflightSkipsRemoteTCP(t *testing.T) {
	t.Parallel()

	cfg := &Config{ServerHost: "100.122.24.50", ServerPort: 3309}
	conn, addr, err, ran := dialServerPreflight(cfg)
	if err != nil {
		t.Fatalf("dialServerPreflight returned error for skipped remote TCP preflight: %v", err)
	}
	if ran {
		t.Fatal("dialServerPreflight ran for a remote TCP server")
	}
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dialServerPreflight returned a connection for a skipped preflight")
	}
	if want := net.JoinHostPort(cfg.ServerHost, "3309"); addr != want {
		t.Fatalf("dialServerPreflight addr = %q, want %q", addr, want)
	}
}

func TestRecordSkippedPreflightResultConnectionFailure(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}

	cfg := &Config{ServerHost: "100.122.24.50", ServerPort: 3309}
	err := recordSkippedPreflightResult(cfg, breaker, errors.New("dial tcp: i/o timeout"))
	if err == nil {
		t.Fatal("recordSkippedPreflightResult returned nil for a connection failure")
	}
	for _, want := range []string{"Dolt server unreachable", "100.122.24.50:3309", "nc -zv"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("connection error missing %q: %v", want, err)
		}
	}
	if got := breaker.State(); got != circuitOpen {
		t.Fatalf("circuit state = %q, want %q", got, circuitOpen)
	}
}

func TestRecordSkippedPreflightResultSemanticFailureDoesNotTrip(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}

	wantErr := errors.New("Error 1045: access denied")
	gotErr := recordSkippedPreflightResult(
		&Config{ServerHost: "100.122.24.50", ServerPort: 3309},
		breaker,
		wantErr,
	)
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("semantic error = %v, want original %v", gotErr, wantErr)
	}
	if strings.Contains(gotErr.Error(), "Dolt server unreachable") {
		t.Fatalf("semantic error was rewritten as a reachability failure: %v", gotErr)
	}
	if got := breaker.State(); got != circuitClosed {
		t.Fatalf("circuit state = %q, want %q", got, circuitClosed)
	}
}

func TestRecordSkippedPreflightResultSuccessResetsBreaker(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}

	if err := recordSkippedPreflightResult(
		&Config{ServerHost: "100.122.24.50", ServerPort: 3309},
		breaker,
		nil,
	); err != nil {
		t.Fatalf("success result returned error: %v", err)
	}

	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	if got := breaker.State(); got != circuitClosed {
		t.Fatalf("circuit state after reset and sub-threshold failures = %q, want %q", got, circuitClosed)
	}
}
