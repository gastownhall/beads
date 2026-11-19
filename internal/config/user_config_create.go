package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrUserConfigExists reports that CreateUserConfigYaml found a file (or a
// symlink, possibly dangling) already at the path: another bd process
// published it first, or the user put something there. Callers re-read the
// path once instead of retrying the create, so a dangling symlink cannot loop.
var ErrUserConfigExists = errors.New("user config.yaml already exists")

// legacyUserConfigReadCap bounds the one-time read of the legacy macOS
// config. A user config is a few hundred bytes; anything larger is not one.
const legacyUserConfigReadCap = 64 << 10

// CreateUserConfigYaml creates the missing user config.yaml at path, which
// must come from UserConfigYamlPath, holding seed. It never replaces an
// existing file: when anything already occupies path it returns an error
// wrapping ErrUserConfigExists.
//
// On darwin/ios it first carries over the config earlier bd releases kept in
// ~/Library/Application Support/bd/config.yaml (os.UserConfigDir there),
// verbatim, so a `bd metrics off` or a node_id stored there survives the move
// to ~/.config (GH#8113). That probe happens only here, when bd is about to
// create the user config, so it runs at most once per creation and never on
// a launch that finds the file. The complete bytes are published with one
// link, so a concurrent bd process sees either no file or the carried one,
// never an empty file it could fill with metrics defaults.
//
// Every other GOOS writes seed with the O_CREATE|O_EXCL create bd has always
// used and never names a legacy path.
func CreateUserConfigYaml(path string, seed []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if !isDarwinUserConfigPlatform() {
		return createExclusive(path, seed)
	}

	// Stage the file before probing, so a directory bd cannot write to never
	// turns into a probe of the legacy path on every launch.
	tmp, err := os.CreateTemp(dir, ".config.yaml.*")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	content := darwinUserConfigContent(seed)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", path, ErrUserConfigExists)
		}
		// A filesystem without hard links: publish the same bytes with the
		// exclusive create instead.
		return createExclusive(path, content)
	}
	return nil
}

func isDarwinUserConfigPlatform() bool {
	return userConfigGOOS == "darwin" || userConfigGOOS == "ios"
}

// darwinUserConfigContent returns the bytes for a new darwin user config: the
// legacy Application Support file's bytes when it is a readable regular file,
// otherwise seed. This is the only code in bd that names that path.
func darwinUserConfigContent(seed []byte) []byte {
	nativeDir, err := osUserConfigDir()
	if err != nil {
		return seed
	}
	legacy := filepath.Join(nativeDir, "bd", "config.yaml")
	info, err := os.Stat(legacy)
	if err != nil || !info.Mode().IsRegular() {
		// Missing, or a directory, FIFO or socket: never open it.
		return seed
	}
	data, err := readCapped(legacy, legacyUserConfigReadCap)
	if err != nil {
		return seed
	}
	return data
}

// readCapped reads at most limit bytes of path and fails if it holds more.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is the fixed legacy user config location
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
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
