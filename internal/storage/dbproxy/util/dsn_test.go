package util

import (
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestDoltServerDSN_PinsMaxAllowedPacket(t *testing.T) {
	dsn := DoltServerDSN{Host: "127.0.0.1", Port: 3306, User: "root"}.String()

	// See internal/storage/doltutil: a zero MaxAllowedPacket makes the driver
	// run "SELECT @@max_allowed_packet" on every new connection. Assert the
	// parsed value, not the DSN text — FormatDSN omits the parameter when it
	// matches the driver default.
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q) failed: %v", dsn, err)
	}
	if cfg.MaxAllowedPacket <= 0 {
		t.Errorf("MaxAllowedPacket should be positive so the driver skips the "+
			"per-connection max_allowed_packet probe; got %d from DSN %q",
			cfg.MaxAllowedPacket, dsn)
	}

	// And must not shrink the client's ceiling below what Dolt accepts.
	const doltServerMaxAllowedPacket = 1 << 30
	if cfg.MaxAllowedPacket < doltServerMaxAllowedPacket {
		t.Errorf("MaxAllowedPacket must be at least Dolt's %d-byte ceiling; got %d",
			doltServerMaxAllowedPacket, cfg.MaxAllowedPacket)
	}
}

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
