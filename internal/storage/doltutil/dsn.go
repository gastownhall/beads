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
// multiStatements=true, interpolateParams=true, allowNativePasswords=true,
// and a connect timeout.
func (d ServerDSN) String() string {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	cfg := mysql.Config{
		User:            d.User,
		Passwd:          d.Password,
		Net:             "tcp",
		Addr:            fmt.Sprintf("%s:%d", d.Host, d.Port),
		DBName:          d.Database,
		ParseTime:       true,
		MultiStatements: true,
		// InterpolateParams collapses each parameterized query from
		// 3 network round-trips (PREPARE + EXECUTE + CLOSE) to 1 by
		// quoting args client-side. Audit (design §5, Commit 2):
		//   - string/int/int64/bool/time.Time/nil: identical wire output
		//   - []byte appears only in federation.go / credentials.go
		//     password paths, driver hex-escapes identically
		//   - zero Prepare/Stmt reuse in non-test code
		//   - no driver.Valuer implementations in internal/types
		//   - no json.RawMessage passed directly as an Exec arg
		//   - orthogonal to MultiStatements (go-sql-driver/mysql
		//     v1.9.3 connection.go:340-410)
		InterpolateParams:    true,
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
