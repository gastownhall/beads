package proxy

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argValue returns the value following the first occurrence of flag in args.
// childArgs always emits flags as separate "--name" "value" tokens, so the
// value is the next element.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestForkArgs_SharedCarriesExternal proves the shared backend's child argv
// carries the --external-* connection flags, exactly as the external backend's
// does. Without them the spawned child would have no managed-dolt target to
// front and would fail to dial its upstream.
func TestForkArgs_SharedCarriesExternal(t *testing.T) {
	opts := OpenOpts{
		Backend:     BackendLocalSharedServer,
		LogFilePath: "/tmp/shared/server.log",
		External: configfile.ExternalDoltConfig{
			Host:            "127.0.0.1",
			Port:            3310,
			TLSRequired:     true,
			KeepAlivePeriod: 45 * time.Second,
		},
	}

	args := childArgs("/tmp/shared/proxieddb", opts, 7777)

	require.Equal(t, "db-proxy-child", args[0])
	backend, ok := argValue(args, "--backend")
	require.True(t, ok)
	assert.Equal(t, string(BackendLocalSharedServer), backend)

	host, ok := argValue(args, "--external-host")
	require.True(t, ok, "shared backend must forward --external-host: %v", args)
	assert.Equal(t, "127.0.0.1", host)

	port, ok := argValue(args, "--external-port")
	require.True(t, ok, "shared backend must forward --external-port")
	assert.Equal(t, "3310", port)

	assert.True(t, hasArg(args, "--external-tls"), "shared backend must forward --external-tls")

	keepAlive, ok := argValue(args, "--external-keep-alive")
	require.True(t, ok)
	assert.Equal(t, (45 * time.Second).String(), keepAlive)
}

// TestForkArgs_ExternalUnchanged guards that extracting the childArgs seam did
// not alter the external backend's argv — the path live portharbour rides today.
func TestForkArgs_ExternalUnchanged(t *testing.T) {
	opts := OpenOpts{
		Backend:     BackendExternal,
		LogFilePath: "/tmp/ext/server.log",
		External:    configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306},
	}
	args := childArgs("/tmp/ext/proxieddb", opts, 6543)

	host, ok := argValue(args, "--external-host")
	require.True(t, ok)
	assert.Equal(t, "db.internal", host)
	port, ok := argValue(args, "--external-port")
	require.True(t, ok)
	assert.Equal(t, "3306", port)
}

// TestForkArgs_LocalServerOmitsExternal guards that the managed local-server
// backend carries NO --external-* flags (it has no external target) but does
// carry its --config.
func TestForkArgs_LocalServerOmitsExternal(t *testing.T) {
	opts := OpenOpts{
		Backend:        BackendLocalServer,
		ConfigFilePath: "/tmp/ls/server_config.yaml",
		LogFilePath:    "/tmp/ls/server.log",
		DoltBinPath:    "/usr/bin/dolt",
	}
	args := childArgs("/tmp/ls/proxieddb", opts, 5432)

	assert.False(t, hasArg(args, "--external-host"), "local-server must not forward --external-host: %v", args)
	cfg, ok := argValue(args, "--config")
	require.True(t, ok)
	assert.Equal(t, "/tmp/ls/server_config.yaml", cfg)
}
