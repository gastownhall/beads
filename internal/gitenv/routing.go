// Package gitenv defines Git environment boundaries shared by command and
// startup discovery code.
package gitenv

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/execenv"
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

// Derive Windows membership once from the canonical routing-key set.
var windowsRoutingKeys = func() map[string]struct{} {
	keys := make(map[string]struct{}, len(routingKeys))
	for key := range routingKeys {
		keys[execenv.KeyIdentityForOS(key, "windows")] = struct{}{}
	}
	return keys
}()

var windowsConfigPrefix = execenv.KeyIdentityForOS("GIT_CONFIG", "windows")

// EntryKey returns the key portion using the shared subprocess split rule.
func EntryKey(entry string) string {
	return execenv.EntryKey(entry)
}

// IsRoutingKeyForOS reports whether key can redirect Git away from an explicit
// working directory or alter its repository, index, object, namespace,
// executable, template, or config authority. Environment names follow host
// semantics: byte-exact on POSIX and case-insensitive on Windows.
func IsRoutingKeyForOS(key, goos string) bool {
	keys := routingKeys
	prefix := "GIT_CONFIG"
	if goos == "windows" {
		keys = windowsRoutingKeys
		prefix = windowsConfigPrefix
	}
	key = execenv.KeyIdentityForOS(key, goos)
	if strings.HasPrefix(key, prefix) {
		return true
	}
	_, routing := keys[key]
	return routing
}

// ScrubRouting removes Git routing entries using the current host's
// environment-key semantics.
func ScrubRouting(env []string) []string {
	return ScrubRoutingForOS(env, runtime.GOOS)
}

// ScrubRoutingForOS removes Git routing entries using goos environment-key
// semantics. It preserves non-routing controls such as GIT_OPTIONAL_LOCKS and
// GIT_NO_REPLACE_OBJECTS, plus explicit system/global config suppression.
// Custom config paths and inline values still lose their routing authority.
func ScrubRoutingForOS(env []string, goos string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		if IsRoutingKeyForOS(EntryKey(entry), goos) && !isConfigSuppression(entry, goos) {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

// isConfigSuppression recognizes controls that disable config sources.
// IsRoutingKeyForOS stays conservative: a key alone cannot distinguish null
// suppression from a custom file that can redirect selected operations.
func isConfigSuppression(entry, goos string) bool {
	key := execenv.KeyIdentityForOS(EntryKey(entry), goos)
	_, value, assigned := strings.Cut(entry, "=")
	if !assigned {
		return false
	}
	switch key {
	case execenv.KeyIdentityForOS("GIT_CONFIG_NOSYSTEM", goos):
		return true // Let Git interpret its Boolean value, including invalid values.
	case execenv.KeyIdentityForOS("GIT_CONFIG_GLOBAL", goos), execenv.KeyIdentityForOS("GIT_CONFIG_SYSTEM", goos):
		return value == "/dev/null" || (goos == "windows" && strings.EqualFold(value, "NUL"))
	}
	return false
}

// ClearRouting permanently removes Git routing entries from the current
// process. CLI callers use it as a command-lifetime authority boundary. It
// retains the same config suppression as ScrubRouting and reports any removal.
func ClearRouting() (bool, error) {
	removed := false
	for _, entry := range os.Environ() {
		key := EntryKey(entry)
		if !IsRoutingKeyForOS(key, runtime.GOOS) || isConfigSuppression(entry, runtime.GOOS) {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			return removed, fmt.Errorf("unset %s: %w", key, err)
		}
		removed = true
	}
	return removed, nil
}
