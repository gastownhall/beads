package storage

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// SQLiteConnString builds a SQLite connection string with standard pragmas.
// It honors the BD_LOCK_TIMEOUT env var for busy timeout (default 30s).
// If readOnly is true, the connection is opened in read-only mode.
// If path is already a file: URI, pragmas are appended only if absent.
func SQLiteConnString(path string, readOnly bool) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	// Best-effort: honor the same env var viper uses (BD_LOCK_TIMEOUT).
	busy := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("BD_LOCK_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			busy = d
		}
	}
	busyMs := int64(busy / time.Millisecond)

	// If it's already a URI, append pragmas if absent.
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
