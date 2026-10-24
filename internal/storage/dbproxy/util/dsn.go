package util

import (
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// maxAllowedPacketBytes is the client-side packet ceiling pinned into every
// DSN this package builds: 1 GiB, matching Dolt's max_allowed_packet default
// and the maximum its sysvar type permits. See
// internal/storage/doltutil.maxAllowedPacketBytes for the full rationale,
// including why this is deliberately not go-sql-driver/mysql's 64 MiB default.
const maxAllowedPacketBytes = 1 << 30

type DoltServerDSN struct {
	Socket          string
	Host            string
	Port            int
	User            string
	Password        string //nolint:gosec // G117: MySQL DSN password field; required by the connection-string builder, not serialized as JSON
	Database        string
	Timeout         time.Duration
	TLSRequired     bool
	TLSCert         string
	TLSKey          string
	TLSConfigName   string
	ClientFoundRows bool
	// AllowCleartextPasswords: intended only alongside TLS (TLSRequired or a
	// registered TLSConfigName). String() fails safe and omits it otherwise;
	// the invariant that REFUSES the combination up front is
	// configfile.ValidateServerAuthConfig / configfile.ExternalDoltConfig.Validate,
	// called by every builder that populates this field.
	AllowCleartextPasswords bool
}

func (d DoltServerDSN) String() string {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// doltutil.ServerAddr, not "%s:%d": a managed server's listener.host may
	// be the IPv6 loopback, which the driver dials only as "[::1]:port",
	// whether the host is written ::1 or [::1].
	network := "tcp"
	addr := doltutil.ServerAddr(d.Host, d.Port)
	if d.Socket != "" {
		network = "unix"
		addr = d.Socket
	}

	tlsOn := d.TLSConfigName != "" || d.TLSRequired
	cfg := mysql.Config{
		User:            d.User,
		Passwd:          d.Password,
		Net:             network,
		Addr:            addr,
		DBName:          d.Database,
		ParseTime:       true,
		MultiStatements: true,
		// Same reason as internal/storage/doltutil.ServerDSN: this config is a
		// composite literal, so without an explicit value FormatDSN emits
		// maxAllowedPacket=0 and the driver spends an extra round-trip on
		// "SELECT @@max_allowed_packet" for every new connection.
		MaxAllowedPacket:     maxAllowedPacketBytes,
		Timeout:              timeout,
		AllowNativePasswords: true,
		ClientFoundRows:      d.ClientFoundRows,
		// Fail safe: a builder that forgot to also resolve/validate TLS gets
		// the driver's own "requires clear text authentication" refusal
		// instead of a DSN that would send the password in the clear.
		AllowCleartextPasswords: d.AllowCleartextPasswords && tlsOn,
	}
	switch {
	case d.TLSConfigName != "":
		cfg.TLSConfig = d.TLSConfigName
	case d.TLSRequired:
		cfg.TLSConfig = "true"
	default:
		cfg.TLSConfig = "false"
	}

	return cfg.FormatDSN()
}
