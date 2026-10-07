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
//
// GIT_CEILING_DIRECTORIES is a deliberate member even though it narrows
// discovery rather than redirecting it: an inherited ceiling can fence off the
// repository the caller is standing in, which is the same loss of working-
// directory authority. Removing it therefore widens the upward search as well,
// so a caller whose working directory is not itself a repository can resolve a
// containing one. That is the intended trade, and pinning an explicit working
// directory does not opt out of it: -C or cmd.Dir fixes where the search
// starts, not where it stops, so once the ceiling is gone the walk above that
// directory is unbounded.
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
//
// Suppression cannot redirect a read, but it can blind one. Callers whose
// result carries authority must use ScrubRoutingAndSuppression instead.
func ScrubRoutingForOS(env []string, goos string) []string {
	return scrubRoutingForOS(env, goos, true)
}

// ScrubRoutingAndSuppression removes Git routing entries using the current
// host's environment-key semantics, including the explicit config suppression
// that ScrubRouting deliberately preserves.
func ScrubRoutingAndSuppression(env []string) []string {
	return ScrubRoutingAndSuppressionForOS(env, runtime.GOOS)
}

// ScrubRoutingAndSuppressionForOS is ScrubRoutingForOS without the config
// suppression exemption: GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_SYSTEM=/dev/null
// and GIT_CONFIG_NOSYSTEM lose their effect along with every other routing key.
//
// Use it wherever a *missing* config value grants privilege rather than merely
// losing a preference. Suppression is harmless for a command that only reports
// what Git sees, but on an authority lookup such as beads.role an inherited
// GIT_CONFIG_NOSYSTEM=1 lets a caller blind the read, and a permissive
// miss-handler (routing.detectFromURL defaults to maintainer) converts that
// blinding into an escalation. Keeping reads and writes of the same key on this
// boundary also stops them from disagreeing about which config file they mean.
func ScrubRoutingAndSuppressionForOS(env []string, goos string) []string {
	return scrubRoutingForOS(env, goos, false)
}

func scrubRoutingForOS(env []string, goos string, keepSuppression bool) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		if IsRoutingKeyForOS(EntryKey(entry), goos) {
			if !keepSuppression || !isConfigSuppressionControl(entry, goos) {
				continue
			}
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

// isConfigSuppressionControl recognizes the config controls that cannot
// redirect Git at a file of the caller's choosing. IsRoutingKeyForOS stays
// conservative: a key alone cannot distinguish null suppression from a custom
// file that can redirect selected operations.
//
// GIT_CONFIG_NOSYSTEM is preserved value-blind — unlike the null-path forms
// below there is no value it can take that names a file, so Git is left to
// interpret its Boolean, including the "false"/"0" spellings that re-enable
// system config and any invalid value it should report itself.
func isConfigSuppressionControl(entry, goos string) bool {
	key := execenv.KeyIdentityForOS(EntryKey(entry), goos)
	_, value, assigned := strings.Cut(entry, "=")
	if !assigned {
		return false
	}
	switch key {
	case execenv.KeyIdentityForOS("GIT_CONFIG_NOSYSTEM", goos):
		return true
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
		if !IsRoutingKeyForOS(key, runtime.GOOS) || isConfigSuppressionControl(entry, runtime.GOOS) {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			return removed, fmt.Errorf("unset %s: %w", key, err)
		}
		removed = true
	}
	return removed, nil
}
