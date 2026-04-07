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
