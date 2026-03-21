package state

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Paths struct {
	ConfigDir       string
	LegacyConfigDir string
	ClientPath      string
	TokensPath      string
	AuthStatePath   string
	BeadsConfigPath string
	BeadsStatePath  string
}

func DefaultPaths() (*Paths, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	legacyDir, err := legacyConfigDir()
	if err != nil {
		return nil, err
	}
	paths := &Paths{
		ConfigDir:       dir,
		LegacyConfigDir: legacyDir,
		ClientPath:      filepath.Join(dir, "client.json"),
		TokensPath:      filepath.Join(dir, "tokens.json"),
		AuthStatePath:   filepath.Join(dir, "auth-state.json"),
		BeadsConfigPath: filepath.Join(dir, "beads.json"),
		BeadsStatePath:  filepath.Join(dir, "beads-state.json"),
	}
	if err := ensureLegacyMigration(paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func configDir() (string, error) {
	if dir := os.Getenv("BEADS_NOTION_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "beads", "notion"), nil
}

func legacyConfigDir() (string, error) {
	if dir := os.Getenv("BDNOTION_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "bdnotion"), nil
}

func ensureLegacyMigration(paths *Paths) error {
	if paths == nil {
		return nil
	}
	if sameDir(paths.ConfigDir, paths.LegacyConfigDir) {
		return nil
	}
	if _, err := os.Stat(paths.ConfigDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(paths.LegacyConfigDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"client.json", "tokens.json", "auth-state.json", "beads.json", "beads-state.json"} {
		src := filepath.Join(paths.LegacyConfigDir, name)
		dst := filepath.Join(paths.ConfigDir, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		//nolint:gosec // One-time migration intentionally copies known legacy config files.
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, fs.FileMode(0o600)); err != nil {
			return err
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
