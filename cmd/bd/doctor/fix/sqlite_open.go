package fix

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func sqliteConnString(path string, readOnly bool) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	busy := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("BD_LOCK_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			busy = d
		}
	}
	busyMs := int64(busy / time.Millisecond)

	if strings.HasPrefix(path, "file:") {
		conn := path
		sep := "?"
		if strings.Contains(conn, "?") {
			sep = "&"
		}
		if readOnly && !strings.Contains(conn, "mode=") {
			conn += sep + "mode=ro"
			sep = "&"
		}
		if !strings.Contains(conn, "_pragma=busy_timeout") {
			conn += fmt.Sprintf("%s_pragma=busy_timeout(%d)", sep, busyMs)
			sep = "&"
		}
		if !strings.Contains(conn, "_pragma=foreign_keys") {
			conn += sep + "_pragma=foreign_keys(ON)"
			sep = "&"
		}
		if !strings.Contains(conn, "_time_format=") {
			conn += sep + "_time_format=sqlite"
		}
		return conn
	}

	if readOnly {
		return fmt.Sprintf("file:%s?mode=ro&_pragma=foreign_keys(ON)&_pragma=busy_timeout(%d)&_time_format=sqlite", path, busyMs)
	}
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=busy_timeout(%d)&_time_format=sqlite", path, busyMs)
}
