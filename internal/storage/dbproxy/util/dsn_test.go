package util

import (
	"strings"
	"testing"
)

func TestDoltServerDSN_TLS(t *testing.T) {
	t.Run("config name takes precedence", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", TLSConfigName: "beads-external-abc", TLSRequired: true}.String()
		if !strings.Contains(dsn, "tls=beads-external-abc") {
			t.Fatalf("dsn %q missing tls=beads-external-abc", dsn)
		}
	})

	t.Run("required without name emits tls=true", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", TLSRequired: true}.String()
		if !strings.Contains(dsn, "tls=true") {
			t.Fatalf("dsn %q missing tls=true", dsn)
		}
	})

	t.Run("default disables tls", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root"}.String()
		if !strings.Contains(dsn, "tls=false") {
			t.Fatalf("dsn %q missing tls=false", dsn)
		}
	})

	t.Run("socket uses unix network", func(t *testing.T) {
		dsn := DoltServerDSN{Socket: "/var/run/dolt.sock", User: "root"}.String()
		if !strings.Contains(dsn, "unix(/var/run/dolt.sock)") {
			t.Fatalf("dsn %q missing unix socket", dsn)
		}
	})
}

func TestDoltServerDSN_AllowCleartextPasswords(t *testing.T) {
	t.Run("enabled alongside TLSRequired", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", TLSRequired: true, AllowCleartextPasswords: true}.String()
		if !strings.Contains(dsn, "allowCleartextPasswords=true") {
			t.Fatalf("dsn %q missing allowCleartextPasswords=true", dsn)
		}
	})

	t.Run("enabled alongside a registered TLSConfigName", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", TLSConfigName: "beads-external-abc", AllowCleartextPasswords: true}.String()
		if !strings.Contains(dsn, "allowCleartextPasswords=true") {
			t.Fatalf("dsn %q missing allowCleartextPasswords=true", dsn)
		}
	})

	t.Run("fails safe without TLS", func(t *testing.T) {
		// String() doesn't refuse the combination — that refusal is
		// configfile.ValidateServerAuthConfig / ExternalDoltConfig.Validate,
		// called by every builder — but it must never emit the flag without
		// TLS, so a missed gate degrades to the driver's own refusal instead
		// of a password sent in the clear.
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", AllowCleartextPasswords: true}.String()
		if strings.Contains(dsn, "allowCleartextPasswords") {
			t.Fatalf("dsn %q should omit allowCleartextPasswords without TLS", dsn)
		}
	})

	t.Run("omitted by default", func(t *testing.T) {
		dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root", TLSRequired: true}.String()
		if strings.Contains(dsn, "allowCleartextPasswords") {
			t.Fatalf("dsn %q should omit allowCleartextPasswords by default", dsn)
		}
	})
}
