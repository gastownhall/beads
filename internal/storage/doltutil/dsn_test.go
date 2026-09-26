package doltutil

import (
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestServerDSN_TLSExplicitlyDisabledByDefault(t *testing.T) {
	dsn := ServerDSN{
		Host: "dolt.example.com",
		Port: 3307,
		User: "root",
	}.String()

	// go-sql-driver/mysql v1.8+ defaults to tls=preferred when TLSConfig
	// is empty. Dolt servers without TLS reject this, so we must explicitly
	// disable TLS when not requested. The formatted DSN should contain
	// tls=false (or the equivalent).
	if !strings.Contains(dsn, "tls=false") {
		t.Errorf("DSN should contain tls=false when TLS is not enabled; got %q", dsn)
	}
}

func TestServerDSN_InterpolatesParams(t *testing.T) {
	dsn := ServerDSN{
		Host: "dolt.example.com",
		Port: 3307,
		User: "root",
	}.String()

	// InterpolateParams collapses parameterized statements from a server-side
	// PREPARE + EXECUTE pair to a single round-trip, which matters over
	// high-latency connections. The formatted DSN must carry it so every
	// connection built from this struct benefits.
	if !strings.Contains(dsn, "interpolateParams=true") {
		t.Errorf("DSN should contain interpolateParams=true; got %q", dsn)
	}
}

func TestServerDSN_PinsMaxAllowedPacket(t *testing.T) {
	dsn := ServerDSN{
		Host: "dolt.example.com",
		Port: 3307,
		User: "root",
	}.String()

	// The driver decides whether to probe the server by testing
	// cfg.MaxAllowedPacket > 0 while establishing each connection; when it is
	// zero it issues "SELECT @@max_allowed_packet" first, costing an extra
	// round-trip per new connection. Assert the effective parsed value rather
	// than the DSN text: FormatDSN deliberately omits the parameter when it
	// equals the driver default, so a substring check would assert the wrong
	// thing and pass for the wrong reason.
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q) failed: %v", dsn, err)
	}
	if cfg.MaxAllowedPacket <= 0 {
		t.Errorf("MaxAllowedPacket should be positive so the driver skips the "+
			"per-connection max_allowed_packet probe; got %d from DSN %q",
			cfg.MaxAllowedPacket, dsn)
	}

	// Skipping the probe must not also shrink what the client will send.
	// Dolt's max_allowed_packet defaults to 1 GiB and its sysvar type caps it
	// there, so anything lower would make the driver reject locally with
	// ErrPktTooLarge statements the server would have accepted — the
	// regression that using the driver's own 64 MiB default would introduce.
	const doltServerMaxAllowedPacket = 1 << 30
	if cfg.MaxAllowedPacket < doltServerMaxAllowedPacket {
		t.Errorf("MaxAllowedPacket must be at least Dolt's %d-byte ceiling so "+
			"the client never rejects a packet the server would accept; got %d",
			doltServerMaxAllowedPacket, cfg.MaxAllowedPacket)
	}
}

func TestServerDSN_UnixSocket(t *testing.T) {
	dsn := ServerDSN{
		Socket: "/tmp/dolt.sock",
		Host:   "should-be-ignored",
		Port:   9999,
		User:   "root",
	}.String()

	if !strings.Contains(dsn, "unix") {
		t.Errorf("DSN should use unix network; got %q", dsn)
	}
	if !strings.Contains(dsn, "/tmp/dolt.sock") {
		t.Errorf("DSN should contain socket path; got %q", dsn)
	}
	// Host:Port should not appear in the DSN address
	if strings.Contains(dsn, "should-be-ignored") || strings.Contains(dsn, "9999") {
		t.Errorf("DSN should ignore Host/Port when Socket is set; got %q", dsn)
	}
}

func TestServerDSN_UnixSocketHonorsTLS(t *testing.T) {
	// TLS over unix sockets is valid (defense-in-depth, client certs).
	// The DSN should respect the TLS setting regardless of transport.
	dsn := ServerDSN{
		Socket: "/tmp/dolt.sock",
		User:   "root",
		TLS:    true,
	}.String()

	if !strings.Contains(dsn, "tls=true") {
		t.Errorf("DSN should honor TLS=true even for unix sockets; got %q", dsn)
	}
}

func TestServerDSN_UnixSocketDefaultTLSOff(t *testing.T) {
	dsn := ServerDSN{
		Socket: "/tmp/dolt.sock",
		User:   "root",
	}.String()

	if !strings.Contains(dsn, "tls=false") {
		t.Errorf("DSN should default to tls=false for unix sockets; got %q", dsn)
	}
}

func TestServerDSN_TCPFallbackWithoutSocket(t *testing.T) {
	dsn := ServerDSN{
		Host: "127.0.0.1",
		Port: 3307,
		User: "root",
	}.String()

	if strings.Contains(dsn, "unix") {
		t.Errorf("DSN should use tcp when Socket is empty; got %q", dsn)
	}
	if !strings.Contains(dsn, "tcp") {
		t.Errorf("DSN should contain tcp network; got %q", dsn)
	}
}

func TestServerDSN_TLSEnabledWhenRequested(t *testing.T) {
	dsn := ServerDSN{
		Host: "hosted.doltdb.com",
		Port: 3307,
		User: "myuser",
		TLS:  true,
	}.String()

	if !strings.Contains(dsn, "tls=true") {
		t.Errorf("DSN should contain tls=true when TLS is enabled; got %q", dsn)
	}
	if strings.Contains(dsn, "tls=false") {
		t.Errorf("DSN should not contain tls=false when TLS is enabled; got %q", dsn)
	}
}
