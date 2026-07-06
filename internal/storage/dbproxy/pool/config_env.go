package pool

import (
	"os"
	"path/filepath"
	"strconv"
)

// Environment variables that gate and tune the connection pooler. The pooler is
// OFF unless EnvEnabled is truthy, so nothing about bd's behavior changes by
// default.
const (
	// EnvEnabled, when set to a truthy value (1/true/yes/on), routes bd's
	// external-Dolt traffic through the query-level connection pooler instead
	// of the TCP byte-passthrough proxy.
	EnvEnabled = "BEADS_DOLT_POOL"
	// EnvSize overrides the number of persistent backend connections the pooler
	// holds open to Dolt (default DefaultSize, clamped to >= MinSize).
	EnvSize = "BEADS_DOLT_POOL_SIZE"
	// EnvSocket overrides the unix socket path the pooler listens on. When
	// unset, a path under the proxy root dir is used.
	EnvSocket = "BEADS_DOLT_POOL_SOCKET"
)

// PoolSocketName is the default pooler socket filename placed under the proxy
// root dir when EnvSocket is unset.
const PoolSocketName = "pool.sock"

// EnabledFromEnv reports whether the pooler is opted in via EnvEnabled.
func EnabledFromEnv() bool {
	return truthy(os.Getenv(EnvEnabled))
}

// SizeFromEnv returns the configured pool size, or 0 when unset/invalid (the
// caller then falls back to DefaultSize).
func SizeFromEnv() int {
	v := os.Getenv(EnvSize)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// SocketFromEnv returns the operator-configured pooler socket path, or a
// default under rootDir when unset.
func SocketFromEnv(rootDir string) string {
	if v := os.Getenv(EnvSocket); v != "" {
		return v
	}
	return filepath.Join(rootDir, PoolSocketName)
}

func truthy(s string) bool {
	switch s {
	case "1", "t", "T", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	default:
		return false
	}
}
