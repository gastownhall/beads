package util

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

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

	net := "tcp"
	addr := fmt.Sprintf("%s:%d", d.Host, d.Port)
	if d.Socket != "" {
		net = "unix"
		addr = d.Socket
	}

	tlsOn := d.TLSConfigName != "" || d.TLSRequired
	cfg := mysql.Config{
		User:                 d.User,
		Passwd:               d.Password,
		Net:                  net,
		Addr:                 addr,
		DBName:               d.Database,
		ParseTime:            true,
		MultiStatements:      true,
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
