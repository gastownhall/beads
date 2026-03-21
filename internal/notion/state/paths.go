package state

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	ConfigDir       string
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
	return &Paths{
		ConfigDir:       dir,
		ClientPath:      filepath.Join(dir, "client.json"),
		TokensPath:      filepath.Join(dir, "tokens.json"),
		AuthStatePath:   filepath.Join(dir, "auth-state.json"),
		BeadsConfigPath: filepath.Join(dir, "beads.json"),
		BeadsStatePath:  filepath.Join(dir, "beads-state.json"),
	}, nil
}

func configDir() (string, error) {
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
