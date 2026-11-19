package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// GH#8113: on macOS, os.UserConfigDir() resolves to
// $HOME/Library/Application Support, so every launch path that called it
// directly stat'ed ~/Library/Application Support/bd/config.yaml — a location
// macOS 26 reports to TCC as an "access data from other apps" prompt. bd's
// documented user-config home is ~/.config/bd, so on darwin/ios the native
// directory is resolved with the same XDG rule Go uses on Unix:
// $XDG_CONFIG_HOME when set and absolute, otherwise $HOME/.config. Without
// XDG the native candidate therefore equals the documented candidate and
// deduplicates away, leaving a single read. Every other GOOS keeps
// os.UserConfigDir() exactly.
//
// The two vars below are the test seams that make darwin/ios behavior
// testable from a Linux host (and vice versa); SetUserConfigPlatformForTest
// is the exported wrapper for out-of-package tests (cmd/bd, doctor).
var (
	userConfigGOOS  = runtime.GOOS
	osUserConfigDir = os.UserConfigDir
)

// UserConfigDir returns the directory bd treats as the native user config
// root (the parent of the "bd" config directory). On darwin/ios it applies
// Go's non-darwin Unix rule — $XDG_CONFIG_HOME when set and absolute, else
// $HOME/.config, erroring exactly the way os.UserConfigDir does on Linux —
// so no bd launch path ever resolves ~/Library/Application Support. On every
// other GOOS it returns os.UserConfigDir()'s value and error unchanged.
func UserConfigDir() (string, error) {
	switch userConfigGOOS {
	case "darwin", "ios":
		return xdgUserConfigDir()
	default:
		return osUserConfigDir()
	}
}

// xdgUserConfigDir mirrors the default (Unix) branch of os.UserConfigDir,
// including its error text, so darwin/ios behave exactly like Linux.
func xdgUserConfigDir() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")
		}
		return home + "/.config", nil
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("path in $XDG_CONFIG_HOME is relative")
	}
	return dir, nil
}

// SetUserConfigPlatformForTest pins the platform UserConfigDir() resolves
// against: goos selects the branch (for example "darwin" or "windows"), and
// native supplies the os.UserConfigDir stand-in used by every non-darwin/ios
// branch. It returns the restore function to defer. It exists so cmd/bd and
// doctor tests can exercise the darwin/ios rule on any host (GH#8113).
func SetUserConfigPlatformForTest(goos string, native func() (string, error)) (restore func()) {
	prevGOOS, prevNative := userConfigGOOS, osUserConfigDir
	userConfigGOOS, osUserConfigDir = goos, native
	return func() {
		userConfigGOOS, osUserConfigDir = prevGOOS, prevNative
	}
}
