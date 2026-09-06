// Package gitenv defines Git environment boundaries shared by command and
// startup discovery code.
package gitenv

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

var routingKeys = map[string]struct{}{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_CEILING_DIRECTORIES":          {},
	"GIT_COMMON_DIR":                   {},
	"GIT_DIR":                          {},
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
	"GIT_EXEC_PATH":                    {},
	"GIT_GRAFT_FILE":                   {},
	"GIT_IMPLICIT_WORK_TREE":           {},
	"GIT_INDEX_FILE":                   {},
	"GIT_INTERNAL_SUPER_PREFIX":        {},
	"GIT_NAMESPACE":                    {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_PREFIX":                       {},
	"GIT_QUARANTINE_PATH":              {},
	"GIT_REPLACE_REF_BASE":             {},
	"GIT_SHALLOW_FILE":                 {},
	"GIT_SUPER_PREFIX":                 {},
	"GIT_TEMPLATE_DIR":                 {},
	"GIT_WORK_TREE":                    {},
}

// EntryKey returns the key portion of an environment entry.
func EntryKey(entry string) string {
	if separator := strings.IndexByte(entry, '='); separator >= 0 {
		return entry[:separator]
	}
	return entry
}

// IsRoutingKeyForOS reports whether key can redirect Git away from an explicit
// working directory or alter its repository, index, object, namespace,
// executable, template, or config authority. Environment names follow host
// semantics: byte-exact on POSIX and case-insensitive on Windows.
func IsRoutingKeyForOS(key, goos string) bool {
	if goos == "windows" {
		key = strings.ToUpper(key)
	}
	if strings.HasPrefix(key, "GIT_CONFIG") {
		return true
	}
	_, blocked := routingKeys[key]
	return blocked
}

// ScrubRouting removes Git routing entries using the current host's
// environment-key semantics.
func ScrubRouting(env []string) []string {
	return ScrubRoutingForOS(env, runtime.GOOS)
}

// ScrubRoutingForOS removes Git routing entries using goos environment-key
// semantics. It preserves non-routing controls such as GIT_OPTIONAL_LOCKS and
// GIT_NO_REPLACE_OBJECTS.
func ScrubRoutingForOS(env []string, goos string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		if IsRoutingKeyForOS(EntryKey(entry), goos) {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

// ClearRouting permanently removes Git routing entries from the current
// process. CLI callers use it as a command-lifetime authority boundary. It
// reports whether any entry was removed.
func ClearRouting() (bool, error) {
	removed := false
	for _, entry := range os.Environ() {
		key := EntryKey(entry)
		if !IsRoutingKeyForOS(key, runtime.GOOS) {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			return removed, fmt.Errorf("unset %s: %w", key, err)
		}
		removed = true
	}
	return removed, nil
}
