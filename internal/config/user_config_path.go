package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// nativeUserConfigValidationSource names the source of a relative directory
// returned without a resolver error. On Unix, os.UserConfigDir already rejects
// relative XDG_CONFIG_HOME values itself; only HOME can reach our validation.
func nativeUserConfigValidationSource() string {
	switch runtime.GOOS {
	case "windows":
		return "APPDATA"
	case "plan9":
		return "home"
	default:
		return "HOME"
	}
}

func buildUserConfigYamlCandidates(homeDir string, homeErr error, nativeConfigDir string, nativeErr error) userConfigYamlCandidates {
	var candidates userConfigYamlCandidates

	if home, err := cleanAbsoluteUserDirectory("user home directory (HOME/USERPROFILE)", homeDir, homeErr); err != nil {
		candidates.homeErr = err
	} else {
		candidates.legacy = filepath.Clean(filepath.Join(home, ".beads", "config.yaml"))
		candidates.documented = filepath.Clean(filepath.Join(home, ".config", "bd", "config.yaml"))
	}

	nativeLabel := "native user config directory"
	if nativeErr == nil {
		nativeLabel += " (" + nativeUserConfigValidationSource() + ")"
	}
	// Resolver errors already identify their source; retain that diagnostic
	// without inferring it from a later read of the process environment.
	if nativeDir, err := cleanAbsoluteUserDirectory(nativeLabel, nativeConfigDir, nativeErr); err != nil {
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
	if path := existingUserConfigYamlPath(candidates); path != "" {
		return path, nil
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

// existingUserConfigYamlPath returns the existing modern user config.yaml —
// the documented file when it exists, otherwise the native one — or "" when
// neither exists. The documented file shadows the native location instead of
// merging with it, and the native candidate is probed only when the
// documented file is absent: statting os.UserConfigDir()/bd/config.yaml on
// every launch raises the macOS 26 app-data privacy prompt even for users
// whose documented config exists (GH#8113). Callers that need a creation
// target rather than an existing file use selectUserConfigYamlPath, which
// adds the documented (or native) preference on top of this.
func existingUserConfigYamlPath(candidates userConfigYamlCandidates) string {
	if userConfigPathExists(candidates.documented) {
		return candidates.documented
	}
	if candidates.native != candidates.documented && userConfigPathExists(candidates.native) {
		return candidates.native
	}
	return ""
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

// userConfigStat is the stat call behind every user-config existence check.
// It is a variable only so tests can observe which candidate paths the
// loader probes; production always stats the real filesystem.
var userConfigStat = os.Stat

func userConfigPathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := userConfigStat(path)
	return err == nil
}
