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

// TestServerDSN_InterpolateParams asserts the DSN enables client-side
// parameter interpolation (see design 2026-04-18 §5 Commit 2) and preserves
// the other non-default flags.
//
// Note: go-sql-driver/mysql's FormatDSN only emits parameters whose value
// differs from the driver's own defaults. AllowNativePasswords defaults to
// true, so it is never serialized into the DSN string even though the
// mysql.Config struct has it set — that's not a regression, just how the
// driver serializes. We therefore only assert on non-default flags.
func TestServerDSN_InterpolateParams(t *testing.T) {
	dsn := ServerDSN{Host: "127.0.0.1", Port: 3307, User: "root"}.String()
	if !strings.Contains(dsn, "interpolateParams=true") {
		t.Fatalf("expected interpolateParams=true in DSN, got: %s", dsn)
	}
	for _, want := range []string{"parseTime=true", "multiStatements=true"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("expected %s in DSN, got: %s", want, dsn)
		}
	}
}
