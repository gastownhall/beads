package doltutil

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// maxAllowedPacketBytes is the client-side packet ceiling pinned into every
// server DSN. See the field comment in ServerDSN.String for why it must be set
// explicitly at all.
//
// The value is 1 GiB, deliberately NOT go-sql-driver/mysql's own 64 MiB
// default. It matches what the server we actually talk to reports: Dolt's SQL
// engine defines max_allowed_packet with Default 1073741824, and its type
// (NewSystemUintType("max_allowed_packet", 1024, 1073741824)) caps the sysvar
// at that same 1 GiB, so no Dolt server can be configured to accept a larger
// packet than this. Pinning the driver default instead would have lowered the
// client's ceiling from ~1 GiB to 64 MiB and made the driver reject
// oversized statements locally with ErrPktTooLarge — a real regression for
// large imports and base64 content, which is why this is not simply
// mysql.NewConfig()'s value.
//
// Trade-off worth stating: the probe this replaces set the ceiling from the
// server's *configured* value, so an operator who lowers max_allowed_packet
// below 1 GiB no longer gets a client-side rejection; the server rejects the
// statement instead. That moves the error from the client to the authoritative
// side and never silently truncates.
const maxAllowedPacketBytes = 1 << 30

// ServerDSN holds connection parameters for building a MySQL DSN to a Dolt server.
// All DSNs built with this struct set parseTime=true and multiStatements=true.
type ServerDSN struct {
	Socket   string // Unix domain socket path; when set, Net="unix" and Host/Port are ignored
	Host     string
	Port     int
	User     string
	Password string        //nolint:gosec // G117: MySQL DSN password field; required by the connection-string builder, not serialized as JSON
	Database string        // optional; empty connects without selecting a database
	Timeout  time.Duration // connect timeout; 0 defaults to 5s
	TLS      bool
}

// String builds the MySQL DSN string. Always sets parseTime=true,
// multiStatements=true, allowNativePasswords=true, and a connect timeout.
func (d ServerDSN) String() string {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	net := "tcp"
	addr := fmt.Sprintf("%s:%d", d.Host, d.Port)
	if d.Socket != "" {
		net = "unix"
		addr = d.Socket
	}

	cfg := mysql.Config{
		User:            d.User,
		Passwd:          d.Password,
		Net:             net,
		Addr:            addr,
		DBName:          d.Database,
		ParseTime:       true,
		MultiStatements: true,
		// InterpolateParams renders bound parameters into the SQL client-side, so
		// a parameterized query is a single round-trip instead of a server-side
		// PREPARE + EXECUTE pair. Over a high-latency connection (e.g. a remote
		// TLS-fronted Dolt server), a write that issues many parameterized
		// statements — such as creating an issue and its labels, events, and
		// dependencies inside one transaction — otherwise pays a full round-trip
		// per statement for the prepare alone. The driver falls back to a
		// server-side prepare (driver.ErrSkip) whenever it cannot safely
		// interpolate an argument, so results never change; this only removes the
		// extra round-trip when interpolation is safe. Independent of
		// MultiStatements. The driver rejects it only with custom unsafe
		// collations, which this DSN never sets.
		InterpolateParams: true,
		// MaxAllowedPacket must be set explicitly because this config is built
		// as a composite literal rather than via mysql.NewConfig(), which is
		// what normally supplies the driver's defaults. Left at the zero value,
		// FormatDSN emits maxAllowedPacket=0, and the driver then runs
		// "SELECT @@max_allowed_packet" while establishing every new connection
		// (connector.go: the probe is taken whenever cfg.MaxAllowedPacket <= 0).
		// That is an extra round-trip per connection, paid on every pool
		// expansion — the cost that matters against a high-latency remote Dolt
		// server, where connections are opened far more often than the packet
		// limit ever changes.
		//
		// See maxAllowedPacketBytes for why the value is the Dolt server's own
		// 1 GiB ceiling rather than the driver's 64 MiB default: pinning must
		// not shrink what the client is willing to send.
		MaxAllowedPacket:     maxAllowedPacketBytes,
		Timeout:              timeout,
		AllowNativePasswords: true,
	}
	if d.TLS {
		cfg.TLSConfig = "true"
	} else {
		// go-sql-driver/mysql v1.8+ defaults to tls=preferred when TLSConfig
		// is empty. Dolt servers without TLS reject preferred-mode negotiation
		// with "TLS requested but server does not support TLS". Explicitly
		// disable TLS so connections work against non-TLS Dolt instances.
		cfg.TLSConfig = "false"
	}

	return cfg.FormatDSN()
}
