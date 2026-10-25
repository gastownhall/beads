//go:build windows

package testbash

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func resolve() (string, error) {
	if override := os.Getenv(OverrideEnv); override != "" {
		if !filepath.IsAbs(override) {
			return "", configurationErrorf("%s must be an absolute path: %q", OverrideEnv, override)
		}
		override = filepath.Clean(override)
		if err := validateCandidate(override); err != nil {
			return "", configurationErrorf("%s=%s: %v", OverrideEnv, override, err)
		}
		return override, nil
	}

	git, err := exec.LookPath("git.exe")
	if err != nil {
		return "", fmt.Errorf("Git for Windows is required: %w", err)
	}
	gitInfo, err := os.Stat(git)
	if err != nil {
		return "", fmt.Errorf("inspect Git for Windows executable %s: %w", git, err)
	}
	if !gitInfo.Mode().IsRegular() {
		return "", fmt.Errorf("Git for Windows executable %s is not an ordinary file", git)
	}

	execPathCommand := exec.Command(git, "--exec-path")
	execPathCommand.Env = sanitizedEnvironment(os.Environ())
	execPathOutput, err := execPathCommand.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"locate the Git for Windows installation through %s: %w: %s",
			git,
			err,
			strings.TrimSpace(string(execPathOutput)),
		)
	}
	execPath := filepath.Clean(strings.TrimSpace(string(execPathOutput)))
	if !filepath.IsAbs(execPath) {
		return "", fmt.Errorf("Git for Windows returned a non-absolute exec path: %q", execPath)
	}

	gitRoot := filepath.Clean(filepath.Join(execPath, "..", "..", ".."))
	candidates := []string{
		filepath.Join(gitRoot, "bin", "bash.exe"),
		filepath.Join(gitRoot, "usr", "bin", "bash.exe"),
	}
	diagnostics := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidateErr := validateCandidate(candidate); candidateErr != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", candidate, candidateErr))
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("no working Git Bash candidate: %s", strings.Join(diagnostics, "; "))
}

func validateCandidate(candidate string) error {
	info, err := os.Stat(candidate)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("is not an ordinary file")
	}

	return runProbe(
		candidate,
		"Git Bash identity",
		`set -eu
test -n "${BASH_VERSION:-}"
case "$(uname -s)" in
  MINGW*|MSYS*) ;;
  *) printf 'expected Git Bash, found %s\n' "$(uname -s)" >&2; exit 69 ;;
esac
pwd -W >/dev/null
`,
		sanitizedEnvironment(os.Environ(), "PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"),
	)
}
