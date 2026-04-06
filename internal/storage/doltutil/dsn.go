package doltutil

import (
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// ServerDSN holds connection parameters for building a MySQL DSN to a Dolt server.
// All DSNs built with this struct set parseTime=true and multiStatements=true.
type ServerDSN struct {
	Host     string
	Port     int
	User     string
	Password string
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

	cfg := mysql.Config{
		User:                 d.User,
		Passwd:               d.Password,
		Net:                  "tcp",
		Addr:                 fmt.Sprintf("%s:%d", d.Host, d.Port),
		DBName:               d.Database,
		ParseTime:            true,
		MultiStatements:      true,
		Timeout:              timeout,
		AllowNativePasswords: true,
	}
	if d.TLS {
		cfg.TLSConfig = "true"
	}

	return cfg.FormatDSN()
}
