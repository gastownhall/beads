package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrUserConfigExists reports that CreateUserConfigYaml found a file (or a
// symlink, possibly dangling) already at the path: another bd process
// published it first, or the user put something there. Callers re-read the
// path once instead of retrying the create, so a dangling symlink cannot loop.
var ErrUserConfigExists = errors.New("user config.yaml already exists")

// CreateUserConfigYaml creates the missing user config.yaml at path, which
// must come from UserConfigYamlPath, holding seed. It never replaces an
// existing file: when anything already occupies path it returns an error
// wrapping ErrUserConfigExists.
func CreateUserConfigYaml(path string, seed []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return createExclusive(path, seed)
}

// createExclusive is the O_CREATE|O_EXCL create the metrics bootstrap has
// always used.
func createExclusive(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is from UserConfigYamlPath
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", path, ErrUserConfigExists)
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
