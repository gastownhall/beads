package metrics

import (
	"os"
	"path/filepath"
	"strings"
)

// machineIDCacheName is the file under ~/.beads that persists the telemetry
// distinct ID. eventkit.MachineID shells out to the platform machine-id probe
// (`ioreg` on macOS, DMI/registry reads elsewhere) via denisbrodbeck/machineid
// on every call — measured at 20.2±1.2ms per bd invocation — so the computed
// ID is cached here once and reused by every subsequent invocation, including
// the detached send-metrics child. The ID is already an app-scoped HMAC of the
// platform machine ID (machineid.ProtectedID), not the raw machine ID, so the
// cache stores nothing more sensitive than what every telemetry event carries;
// it is still written 0600 like the rest of our per-user state.
const machineIDCacheName = "machine-id"

// maxMachineIDLen bounds what the cache read will accept. ProtectedID today is
// a 64-char hex HMAC; the bound is loose so an upstream format change does not
// silently invalidate every cache, while still refusing to feed a corrupt or
// swapped-in file into every event's distinct_id.
const maxMachineIDLen = 128

func machineIDCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dataDirName, machineIDCacheName), nil
}

// validMachineID accepts a cached or freshly computed ID for (re)use: one
// non-empty token of printable non-space ASCII, bounded length, and not the
// literal "invalid" that eventkit.MachineID returns when the platform probe
// fails — a failed probe must be retried next run, never cached.
func validMachineID(id string) bool {
	if id == "" || id == "invalid" || len(id) > maxMachineIDLen {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

func readCachedMachineID(path string) string {
	// #nosec G304 -- path is derived from os.UserHomeDir + our fixed cache
	// name (machineIDCachePath), never from user or repository input.
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if !validMachineID(id) {
		return ""
	}
	return id
}

// writeMachineIDCache persists id atomically (temp file + rename) so a
// concurrent reader can never observe a truncated ID. Failures are ignored:
// the cache is a pure optimization and the caller already holds a usable ID.
func writeMachineIDCache(path, id string) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, machineIDCacheName+".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
	}
}

// cachedMachineID returns the stable distinct ID for this machine, reading the
// ~/.beads/machine-id cache first and falling back to the (slow) platform
// probe, whose result it caches for every later invocation. Only called when
// metrics are enabled — a disabled invocation never pays for an ID at all.
//
// The probe itself (computeMachineID, backed by eventkit.MachineID) lives in
// metrics.go: eventkit imports are depguard-fenced to metrics.go/flusher.go
// (.golangci.yml dolt-storage-boundary), and this file needs none of it.
func cachedMachineID(appName string) string {
	path, err := machineIDCachePath()
	if err != nil {
		return computeMachineID(appName)
	}
	if id := readCachedMachineID(path); id != "" {
		return id
	}
	id := computeMachineID(appName)
	if validMachineID(id) {
		writeMachineIDCache(path, id)
	}
	return id
}
