package util

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

type DoltServerDSN struct {
	Socket      string
	Host        string
	Port        int
	User        string
	Password    string //nolint:gosec // G117: MySQL DSN password field; required by the connection-string builder, not serialized as JSON
	Database    string
	Timeout     time.Duration
	TLSRequired bool
	TLSCert     string
	TLSKey      string
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
		User:                 d.User,
		Passwd:               d.Password,
		Net:                  net,
		Addr:                 addr,
		DBName:               d.Database,
		ParseTime:            true,
		MultiStatements:      true,
		Timeout:              timeout,
		AllowNativePasswords: true,
	}
	if d.TLSRequired {
		cfg.TLSConfig = "true"
	} else {
		cfg.TLSConfig = "false"
	}

	return cfg.FormatDSN()
}
