package doltutil

import (
	"strings"
	"testing"
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
