// Package testbash resolves the Bash interpreter used by repository script
// tests. On Windows, callers receive a positively identified Git Bash rather
// than whichever bash launcher happens to appear first on PATH.
package testbash

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// OverrideEnv names the optional absolute Git Bash override used on Windows.
const OverrideEnv = "BEADS_TEST_GIT_BASH"

const probeSuccess = "BEADS_TEST_BASH_PROBE_SUCCESS_7B75D507"

// ConfigurationError reports an invalid explicit test configuration. Callers
// may distinguish it from an unavailable optional prerequisite.
type ConfigurationError struct {
	err error
}

func (e *ConfigurationError) Error() string {
	return e.err.Error()
}

func (e *ConfigurationError) Unwrap() error {
	return e.err
}

// IsConfigurationError reports whether err was caused by an invalid explicit
// test configuration.
func IsConfigurationError(err error) bool {
	var configurationError *ConfigurationError
	return errors.As(err, &configurationError)
}

func configurationErrorf(format string, args ...any) error {
	return &ConfigurationError{err: fmt.Errorf(format, args...)}
}

// Resolve returns the Bash interpreter for a repository script test.
func Resolve() (string, error) {
	return resolve()
}

// Probe verifies a caller-specific Bash capability under an environment
// derived from the supplied entries. Bash startup, option, and
// exported-function controls are removed before the probe runs.
func Probe(path, capability, script string, environment []string) error {
	return runProbe(path, capability, script, sanitizedEnvironment(environment))
}

func runProbe(path, capability, script string, environment []string) error {
	probeScript := script + `
beads_testbash_probe_status=$?
if [ "$beads_testbash_probe_status" -eq 0 ]; then
  builtin printf '%s' '` + probeSuccess + `'
fi
exit "$beads_testbash_probe_status"
`
	//nolint:gosec // G702: this test-only API intentionally executes its caller-selected Bash interpreter.
	cmd := exec.Command(path, "--noprofile", "--norc", "-c", probeScript)
	cmd.Env = environment
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s probe: %w: %s", capability, err, strings.TrimSpace(string(output)))
	}
	if string(output) != probeSuccess {
		return fmt.Errorf(
			"%s probe exited successfully without the exact execution sentinel: %q",
			capability,
			string(output),
		)
	}
	return nil
}

func sanitizedEnvironment(entries []string, overrides ...string) []string {
	overrideKeys := make(map[string]struct{}, len(overrides))
	safeOverrides := make([]string, 0, len(overrides))
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		normalizedKey := environmentKey(key)
		if isBashAuthorityControl(normalizedKey) {
			continue
		}
		overrideKeys[normalizedKey] = struct{}{}
		safeOverrides = append(safeOverrides, entry)
	}

	environment := make([]string, 0, len(entries)+len(safeOverrides))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		normalizedKey := environmentKey(key)
		if isBashAuthorityControl(normalizedKey) {
			continue
		}
		if _, overridden := overrideKeys[normalizedKey]; overridden {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, safeOverrides...)
}

func isBashAuthorityControl(normalizedKey string) bool {
	return normalizedKey == environmentKey("BASH_ENV") ||
		normalizedKey == environmentKey("ENV") ||
		normalizedKey == environmentKey("SHELLOPTS") ||
		normalizedKey == environmentKey("BASHOPTS") ||
		normalizedKey == environmentKey("GIT_EXEC_PATH") ||
		strings.HasPrefix(normalizedKey, environmentKey("BASH_FUNC_"))
}

func environmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}
