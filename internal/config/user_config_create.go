package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// GH#8113: on darwin/ios, older bd releases kept the user config at
// ~/Library/Application Support/bd/config.yaml (Go's os.UserConfigDir). bd no
// longer reads that location on any launch path; CreateUserConfigYaml below
// is the only code that names it, once, at the moment bd first creates the
// documented file — carrying a readable legacy config over verbatim so an
// existing user (and in particular a metrics opt-out) keeps their settings.

// ErrUserConfigAlreadyExists reports that another process published the user
// config between the caller's not-exist observation and the create. The
// caller must treat the file as existing and re-read it once — never retry
// the create in a loop (a dangling symlink makes both observations disagree
// forever, which used to recurse until the stack ran out).
var ErrUserConfigAlreadyExists = errors.New("user config already exists")

// ErrUserConfigConsentUnknown reports that bd could not create the user
// config file, so a legacy opt-out could be neither confirmed nor ruled out.
// It is produced only on darwin/ios, where the carry-over is the only way to
// learn an opt-out: a run whose bootstrap fails this way must not turn
// metrics on (see resolveMetricsEnabled in cmd/bd).
var ErrUserConfigConsentUnknown = errors.New("user config consent unknown")

// legacyUserConfigMaxBytes bounds the one-time carry-over read of the legacy
// macOS config: a file larger than this is treated as unreadable (fail
// closed) rather than copied.
const legacyUserConfigMaxBytes = 64 << 10 // 64 KiB

// probeLegacyStat and probeLegacyReadFile are the legacy-probe seams. Only
// in-package tests replace them (to count probes of the Application Support
// path); production always uses os.Stat/os.ReadFile.
var (
	probeLegacyStat     = os.Stat
	probeLegacyReadFile = os.ReadFile
)

// legacyUserConfigYamlPath is the single place bd names the pre-GH#8113
// macOS user config: <os.UserConfigDir()>/bd/config.yaml, which on darwin is
// ~/Library/Application Support/bd/config.yaml. It is consulted only by the
// one-time carry-over in CreateUserConfigYaml, never on any launch path.
func legacyUserConfigYamlPath() (string, error) {
	dir, err := osUserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(dir, "bd", "config.yaml")), nil
}

// userConfigFailClosedSeed is what the carry-over publishes when the legacy
// file exists but bd cannot read it — for example EPERM after a TCC denial.
// Metrics stay off: an opt-out bd cannot read must not be overridden
// (GH#8113). The comment deliberately does not match the commented
// "metrics:" block pattern (commentedMetricsRe in internal/metrics), so the
// bootstrap's existing-file logic still sees the explicit metrics section.
func userConfigFailClosedSeed(legacyPath string) []byte {
	comment := fmt.Sprintf("# bd could not read the previous macOS user config at %s (GH#8113); run \"bd metrics on\" to enable metrics", legacyPath)
	return []byte(comment + "\nmetrics:\n  disabled: true\n")
}

// CreateUserConfigYaml creates the user config file at path with seed bytes
// when the caller has observed it not to exist. On darwin/ios it probes the
// legacy macOS file once, at this creation moment only, and publishes the
// legacy bytes verbatim when they are readable (preserving
// metrics.disabled/endpoint/notice_shown, node_id, actor, mail.delegate and
// comments); the publish is a single atomic link, so concurrent first runs
// can never observe a half-written config or recopy over a peer's decision.
// On every other GOOS it writes seed with the historical O_CREATE|O_EXCL
// semantics and never names a legacy path.
//
// Errors wrap ErrUserConfigAlreadyExists when another process published the
// file first (re-read it once; do not retry), and, on darwin/ios only,
// ErrUserConfigConsentUnknown when the file could not be created — in which
// case the legacy path was never probed.
func CreateUserConfigYaml(path string, seed []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return userConfigCreateError(fmt.Errorf("mkdir %s: %w", dir, err))
	}
	if isDarwinUserConfigPlatform() {
		return createUserConfigYamlDarwin(path, seed)
	}
	return createUserConfigYamlExclusive(path, seed)
}

// isDarwinUserConfigPlatform reports whether the platform seam currently
// selects the darwin/ios branch (the carry-over, the consent-unknown
// sentinel and the fail-closed seed all live there).
func isDarwinUserConfigPlatform() bool {
	return userConfigGOOS == "darwin" || userConfigGOOS == "ios"
}

// userConfigCreateError marks a creation failure as consent-unknown on
// darwin/ios: the run could not learn a legacy opt-out, so it must fail
// closed. Elsewhere the error is returned unchanged (Linux and Windows
// bootstrap behavior is unchanged by GH#8113).
func userConfigCreateError(err error) error {
	if isDarwinUserConfigPlatform() {
		return errors.Join(err, ErrUserConfigConsentUnknown)
	}
	return err
}

func createUserConfigYamlDarwin(path string, seed []byte) error {
	dir := filepath.Dir(path)
	// The temp file comes first: when the config directory is unwritable,
	// bd fails here — before the probe — so a permanently uncreatable config
	// never costs an Application Support read on any launch (GH#8113).
	tmp, err := os.CreateTemp(dir, ".bd-config-*.tmp")
	if err != nil {
		return userConfigCreateError(fmt.Errorf("create temp file in %s: %w", dir, err))
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	body := seed
	if legacyPath, legacyErr := legacyUserConfigYamlPath(); legacyErr == nil && legacyPath != "" {
		body = carryOverLegacyUserConfig(legacyPath, seed)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrUserConfigAlreadyExists
		}
		// A filesystem without hard-link support: fall back to the
		// exclusive create with the same bytes. Its EEXIST is translated
		// below, so the caller still re-reads rather than recursing.
		return createUserConfigYamlExclusive(path, body)
	}
	return nil
}

// carryOverLegacyUserConfig returns the bytes to publish in the new user
// config: the legacy file's contents verbatim when it is a readable regular
// file within the size cap (a symlink is followed); seed when there is no
// readable legacy file (absent, or a directory/FIFO/socket, which is never
// opened, so a FIFO cannot block); and the fail-closed metrics-off seed when
// the legacy file exists but cannot be read or is over the cap.
func carryOverLegacyUserConfig(legacyPath string, seed []byte) []byte {
	info, err := probeLegacyStat(legacyPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return seed
		}
		return userConfigFailClosedSeed(legacyPath)
	}
	if !info.Mode().IsRegular() {
		return seed
	}
	if info.Size() > legacyUserConfigMaxBytes {
		return userConfigFailClosedSeed(legacyPath)
	}
	data, err := probeLegacyReadFile(legacyPath)
	if err != nil {
		return userConfigFailClosedSeed(legacyPath)
	}
	return data
}

func createUserConfigYamlExclusive(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is from config.UserConfigYamlPath
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrUserConfigAlreadyExists
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
