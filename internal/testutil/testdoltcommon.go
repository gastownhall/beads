package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"time"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
const DoltDockerImage = "dolthub/dolt-sql-server:1.83.0"

// FindFreePort finds an available TCP port by binding to :0.
func FindFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// WaitForServer polls until the server accepts TCP connections on the given port.
func WaitForServer(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		// #nosec G704 -- addr is always loopback (127.0.0.1) with a test-selected local port.
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// WaitForSQLServer polls until the Dolt SQL server on the given port can
// complete a root connection and answer SHOW DATABASES. This is stricter than
// WaitForServer(): Docker can expose the TCP port before the SQL layer is ready,
// which makes early test connections fail with unexpected EOF / invalid
// connection even though a raw dial succeeds.
func WaitForSQLServer(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/?parseTime=true&timeout=1s&readTimeout=1s&writeTimeout=1s", port)
	var lastErr error
	consecutiveSuccesses := 0
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			rows, queryErr := db.QueryContext(ctx, "SHOW DATABASES")
			cancel()
			if rows != nil {
				_ = rows.Close()
			}
			_ = db.Close()
			if queryErr == nil {
				consecutiveSuccesses++
				if consecutiveSuccesses >= 3 {
					return nil
				}
				time.Sleep(200 * time.Millisecond)
				continue
			}
			consecutiveSuccesses = 0
			lastErr = queryErr
		} else {
			consecutiveSuccesses = 0
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for SQL readiness")
	}
	return fmt.Errorf("waiting for Dolt SQL server on 127.0.0.1:%d: %w", port, lastErr)
}
