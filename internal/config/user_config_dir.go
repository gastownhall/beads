package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// userConfigGOOS and osUserConfigDir are seams so tests on any host can drive
// the darwin/ios branch of UserConfigDir. osUserConfigDir is the only route to
// Go's os.UserConfigDir in bd; on darwin/ios it names the legacy
// ~/Library/Application Support root, which bd reads at most once, when it
// creates the user config file (see createUserConfigYaml).
var (
	userConfigGOOS  = runtime.GOOS
	osUserConfigDir = os.UserConfigDir
)

// UserConfigDir returns bd's native user configuration root.
//
// On darwin and ios it applies the same rule Go uses on other Unix systems:
// $XDG_CONFIG_HOME when set and absolute, an error when set but relative, and
// otherwise $HOME/.config. Go's os.UserConfigDir returns
// ~/Library/Application Support there, and statting files under it on every
// bd launch raises a macOS privacy (TCC) prompt (GH#8113). Without
// XDG_CONFIG_HOME the native root is then the documented ~/.config root, so
// the user config merge reads one file instead of two.
//
// On every other GOOS it returns os.UserConfigDir's value and error exactly.
func UserConfigDir() (string, error) {
	switch userConfigGOOS {
	case "darwin", "ios":
		return unixUserConfigDir()
	default:
		return osUserConfigDir()
	}
}

func unixUserConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("path in $XDG_CONFIG_HOME is relative")
		}
		return dir, nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")
	}
	return home + "/.config", nil
}

// SetUserConfigPlatformForTest makes UserConfigDir behave as on goos, with
// native standing in for os.UserConfigDir (on darwin/ios it is consulted only
// to name the legacy Application Support config file). A nil native keeps the
// current function. The returned func restores the previous values. Tests only.
func SetUserConfigPlatformForTest(goos string, native func() (string, error)) (restore func()) {
	prevGOOS, prevNative := userConfigGOOS, osUserConfigDir
	userConfigGOOS = goos
	if native != nil {
		osUserConfigDir = native
	}
	return func() {
		userConfigGOOS, osUserConfigDir = prevGOOS, prevNative
	}
}
