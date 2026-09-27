package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const userConfigYamlDisplayFallback = "~/.config/bd/config.yaml"

// userConfigYamlCandidates contains only cleaned, absolute paths suitable for
// the current operating system's filesystem APIs. A missing candidate means
// its source could not be resolved safely.
type userConfigYamlCandidates struct {
	legacy     string // <home>/.beads/config.yaml
	native     string // <os.UserConfigDir()>/bd/config.yaml
	documented string // <home>/.config/bd/config.yaml

	homeErr   error
	nativeErr error
}

func currentUserConfigYamlCandidates() userConfigYamlCandidates {
	homeDir, homeErr := os.UserHomeDir()
	nativeConfigDir, nativeErr := os.UserConfigDir()
	return buildUserConfigYamlCandidates(homeDir, homeErr, nativeConfigDir, nativeErr)
}

func buildUserConfigYamlCandidates(homeDir string, homeErr error, nativeConfigDir string, nativeErr error) userConfigYamlCandidates {
	var candidates userConfigYamlCandidates

	if home, err := cleanAbsoluteUserDirectory("user home directory", homeDir, homeErr); err != nil {
		candidates.homeErr = err
	} else {
		candidates.legacy = filepath.Clean(filepath.Join(home, ".beads", "config.yaml"))
		candidates.documented = filepath.Clean(filepath.Join(home, ".config", "bd", "config.yaml"))
	}

	if nativeDir, err := cleanAbsoluteUserDirectory("native user config directory", nativeConfigDir, nativeErr); err != nil {
		candidates.nativeErr = err
	} else {
		candidates.native = filepath.Clean(filepath.Join(nativeDir, "bd", "config.yaml"))
	}

	return candidates
}

func cleanAbsoluteUserDirectory(label, path string, resolutionErr error) (string, error) {
	if resolutionErr != nil {
		return "", fmt.Errorf("%s: %w", label, resolutionErr)
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%s %q is not an absolute native path", label, path)
	}
	return cleaned, nil
}

// UserConfigYamlPath resolves the user-level config.yaml to a cleaned,
// absolute path suitable for native filesystem APIs. It prefers the documented
// <home>/.config/bd location when that file exists, then an existing native
// os.UserConfigDir location. For a new file it keeps the documented location
// as the creation target when possible, falling back to the native location
// only when the home directory itself cannot be resolved safely.
func UserConfigYamlPath() (string, error) {
	return selectUserConfigYamlPath(currentUserConfigYamlCandidates())
}

func selectUserConfigYamlPath(candidates userConfigYamlCandidates) (string, error) {
	if userConfigPathExists(candidates.documented) {
		return candidates.documented, nil
	}
	if candidates.native != candidates.documented && userConfigPathExists(candidates.native) {
		return candidates.native, nil
	}
	if candidates.documented != "" {
		return candidates.documented, nil
	}
	if candidates.native != "" {
		return candidates.native, nil
	}

	err := errors.Join(candidates.homeErr, candidates.nativeErr)
	if err == nil {
		err = errors.New("no absolute native user directory is available")
	}
	return "", fmt.Errorf("resolve user config.yaml: %w", err)
}

// UserConfigYamlDisplayPath returns a human-readable location for command
// output. The tilde form is deliberately confined to this display-only API and
// must never be passed to a filesystem operation.
func UserConfigYamlDisplayPath() string {
	return userConfigYamlDisplayPath(currentUserConfigYamlCandidates())
}

func userConfigYamlDisplayPath(candidates userConfigYamlCandidates) string {
	if path, err := selectUserConfigYamlPath(candidates); err == nil {
		return path
	}
	return userConfigYamlDisplayFallback
}

func userConfigPathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
