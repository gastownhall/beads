package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	if IsMacOSFamily(goos) {
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
	}
	return osUserConfigDir()
}

// IsMacOSFamily reports whether goos is an Apple platform (darwin or ios),
// where bd's user config directory follows the Unix rule instead of
// ~/Library/Application Support (GH#8113). Every GH#8113 platform decision
// (the resolver, the metrics carry-over and post-bootstrap consent, the doctor
// check) asks this one question.
func IsMacOSFamily(goos string) bool {
	return goos == "darwin" || goos == "ios"
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

// LegacyDarwinUserConfigYamlPath returns
// <home>/Library/Application Support/bd/config.yaml, the pre-GH#8113 macOS
// user config that bd no longer merges. home comes from os.UserHomeDir and
// must be absolute. It only computes the path: the one-time metrics bootstrap
// carry-over and the macOS doctor check are the only callers allowed to touch
// the file, because macOS 26 can guard that folder with a privacy prompt.
func LegacyDarwinUserConfigYamlPath() (string, error) {
	homeDir, homeErr := os.UserHomeDir()
	home, err := cleanAbsoluteUserDirectory("user home directory (HOME/USERPROFILE)", homeDir, homeErr)
	if err != nil {
		return "", err
	}
	return LegacyDarwinUserConfigYamlPathFor(home), nil
}

// LegacyDarwinUserConfigYamlPathFor is LegacyDarwinUserConfigYamlPath for an
// already-resolved absolute home directory.
func LegacyDarwinUserConfigYamlPathFor(home string) string {
	return filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
}

// LegacyDarwinUserConfigMaxBytes bounds a read of the legacy macOS user
// config; a real bd user config is a few hundred bytes.
const LegacyDarwinUserConfigMaxBytes = 1 << 20

// ReadLegacyDarwinUserConfigYaml reads the legacy macOS user config at path
// for its two sanctioned readers (the metrics bootstrap carry-over and the
// macOS doctor check). It stats before opening, so a FIFO or device there is
// refused without the open that could block, opens non-blocking on Unix (so a
// FIFO swapped in after the stat cannot block either), re-checks the opened
// file, and
// refuses anything larger than LegacyDarwinUserConfigMaxBytes. A missing file
// returns an error that matches fs.ErrNotExist.
func ReadLegacyDarwinUserConfigYaml(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := checkLegacyDarwinUserConfigInfo(path, info); err != nil {
		return nil, err
	}
	// Open non-blocking where the platform has it: a FIFO swapped in after
	// the stat then opens without waiting for a writer, and the fstat
	// re-check below refuses it.
	f, err := openLegacyDarwinUserConfig(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Re-check the opened file: the path may have been swapped since the stat.
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	if err := checkLegacyDarwinUserConfigInfo(path, info); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, LegacyDarwinUserConfigMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > LegacyDarwinUserConfigMaxBytes {
		return nil, legacyTooLargeError(path)
	}
	return data, nil
}

// checkLegacyDarwinUserConfigInfo refuses a legacy config that is not a
// regular file or is over the size cap. Its errors are *fs.PathError, like
// the open and read errors, so a caller that prints the path itself can print
// just the reason.
func checkLegacyDarwinUserConfigInfo(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return &fs.PathError{Op: "read", Path: path, Err: errors.New("not a regular file")}
	}
	if info.Size() > LegacyDarwinUserConfigMaxBytes {
		return legacyTooLargeError(path)
	}
	return nil
}

func legacyTooLargeError(path string) error {
	return &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("larger than %d bytes", LegacyDarwinUserConfigMaxBytes)}
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
