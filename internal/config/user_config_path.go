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
	native     string // <UserConfigDir()>/bd/config.yaml
	documented string // <home>/.config/bd/config.yaml

	homeErr   error
	nativeErr error
}

// userConfigDir is the resolver behind UserConfigDir; tests replace it with
// SetUserConfigDirForTest.
var userConfigDir = func() (string, error) {
	return userConfigDirFor(runtime.GOOS, os.Getenv, os.UserConfigDir)
}

// statUserConfigPath is the only stat behind userConfigPathExists. Tests
// record it to prove which user config paths a read probes.
var statUserConfigPath = os.Stat

// UserConfigDir returns the root of bd's per-user configuration (bd/config.yaml
// and beads/PRIME.md live under it). It is os.UserConfigDir on every platform
// except darwin and ios, where it follows the Unix rule instead
// ($XDG_CONFIG_HOME when set, which must be absolute, else $HOME/.config), so
// bd never touches ~/Library/Application Support, which macOS 26 can guard
// with a privacy prompt (GH#8113).
func UserConfigDir() (string, error) { return userConfigDir() }

// SetUserConfigDirForTest replaces the UserConfigDir resolver and returns a
// function that restores it. Not safe for parallel tests.
func SetUserConfigDirForTest(fn func() (string, error)) (restore func()) {
	prev := userConfigDir
	userConfigDir = fn
	return func() { userConfigDir = prev }
}

// userConfigDirFor is UserConfigDir with its inputs injected. On darwin and
// ios it is Go's os.UserConfigDir Unix branch verbatim (no trimming, no
// cleaning); everywhere else it returns osUserConfigDir() itself, value and
// error, so non-Apple platforms are unchanged.
func userConfigDirFor(goos string, getenv func(string) string, osUserConfigDir func() (string, error)) (string, error) {
	switch goos {
	case "darwin", "ios":
		if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
			if !filepath.IsAbs(dir) {
				return "", errors.New("path in $XDG_CONFIG_HOME is relative")
			}
			return dir, nil
		}
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")
		}
		return home + "/.config", nil
	default:
		return osUserConfigDir()
	}
}

func currentUserConfigYamlCandidates() userConfigYamlCandidates {
	homeDir, homeErr := os.UserHomeDir()
	nativeConfigDir, nativeErr := userConfigDir()
	return buildUserConfigYamlCandidates(homeDir, homeErr, nativeConfigDir, nativeErr)
}

// UserConfigYamlSearchPaths returns the user-level config.yaml paths bd merges,
// lowest precedence first (legacy, native, documented): cleaned, absolute,
// deduplicated, unresolvable ones omitted. It never touches the filesystem.
func UserConfigYamlSearchPaths() []string {
	candidates := currentUserConfigYamlCandidates()
	var paths []string
	for _, path := range []string{candidates.legacy, candidates.native, candidates.documented} {
		if path == "" {
			continue
		}
		duplicate := false
		for _, existing := range paths {
			if existing == path {
				duplicate = true
				break
			}
		}
		if !duplicate {
			paths = append(paths, path)
		}
	}
	return paths
}

// nativeUserConfigValidationSource names the source of a relative directory
// returned without a resolver error. The resolver itself rejects relative
// XDG_CONFIG_HOME values (os.UserConfigDir on Unix, UserConfigDir's own rule
// on darwin and ios); only HOME can reach our validation there.
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
// UserConfigDir location. For a new file it keeps the documented location
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
	_, err := statUserConfigPath(path)
	return err == nil
}
