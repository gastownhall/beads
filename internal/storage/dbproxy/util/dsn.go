package util

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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
}

func (d DoltServerDSN) String() string {
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
		// Same reason as internal/storage/doltutil.ServerDSN: this config is a
		// composite literal, so without an explicit value FormatDSN emits
		// maxAllowedPacket=0 and the driver spends an extra round-trip on
		// "SELECT @@max_allowed_packet" for every new connection.
		MaxAllowedPacket:     maxAllowedPacketBytes,
		Timeout:              timeout,
		AllowNativePasswords: true,
		ClientFoundRows:      d.ClientFoundRows,
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
