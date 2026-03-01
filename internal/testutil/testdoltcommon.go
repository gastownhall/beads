package testutil

import (
	"fmt"
	"net"
	"time"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
// Pinned to 1.43.0 because Dolt >= 1.44 has a broken auth handshake:
// root@localhost vs root@% — the go-sql-driver connects via TCP mapped port
// which maps to root@%, but only root@localhost exists. The Docker image
// does not process /docker-entrypoint-initdb.d/ scripts, so WithScripts
// can't work around it. See testdata/dolt-init.sql for the workaround that
// would work if the image supported init scripts.
// Tracked upstream with DoltHub; bump when fixed.
const DoltDockerImage = "dolthub/dolt-sql-server:1.43.0"

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
