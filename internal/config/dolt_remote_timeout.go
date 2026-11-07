package config

import (
	"os"
	"strings"
	"time"
)

// DefaultDoltRemoteTimeout is the embedded remote transfer budget when
// dolt.remote-timeout is unset or unusable. It matches the server-mode CLI
// transfer default (BEADS_CLI_TRANSFER_TIMEOUT).
const DefaultDoltRemoteTimeout = 5 * time.Minute

// doltRemoteTimeoutEnv overrides dolt.remote-timeout.
const doltRemoteTimeoutEnv = "BD_DOLT_REMOTE_TIMEOUT"

// DoltRemoteTimeout returns the budget for one embedded remote transfer
// statement (DOLT_PUSH or DOLT_FETCH; GH#7989). BD_DOLT_REMOTE_TIMEOUT, when
// set, takes precedence over dolt.remote-timeout. A missing, unparsable, zero
// or negative value falls back to DefaultDoltRemoteTimeout, and so does a read
// before Initialize unless the environment supplies a value: SDK callers that
// never initialize configuration still get a bounded transfer.
func DoltRemoteTimeout() time.Duration {
	raw, ok := os.LookupEnv(doltRemoteTimeoutEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		raw = GetString("dolt.remote-timeout")
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return DefaultDoltRemoteTimeout
	}
	return d
}
