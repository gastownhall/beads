package metrics

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/lockfile"
)

var commentedMetricsRe = regexp.MustCompile(`(?m)^\s*#\s*metrics\s*:`)

// EnsureUserConfigDefaults writes metrics.disabled and metrics.endpoint into
// the user config bd reads when either is absent.
//
// On macOS (darwin and ios), when metrics.disabled is about to be written, it
// first carries a metrics choice over from the pre-GH#8113 user config in
// ~/Library/Application Support (see legacyDarwinMetricsCarryOver). The probe
// runs only under the <config>.lock lock (created beside the config, or beside
// its symlink target, and left in place), and only after the write is shown
// to be possible: the directory or file is writable and, for an existing
// config, the metrics keys fit its YAML shape (checkMetricsKeysWritable). A
// successful write leaves metrics.disabled present, so every later run returns
// before the lock and the probe happens once per user config. A config bd
// cannot write, cannot parse, whose metrics block is commented out, or whose
// shape refuses the keys never triggers the probe; the caller then keeps
// metrics off for the run (see cmd/bd metricsEnabledAfterBootstrap).
//
// Elsewhere there is nothing to probe, no lock and no shape check: the
// hard-link publish in createUserConfig already makes creating the file
// atomic.
func EnsureUserConfigDefaults() error {
	path, err := config.UserConfigYamlPath()
	if err != nil {
		return fmt.Errorf("ensure user config: %w", err)
	}

	state, err := readMetricsKeyState(path)
	if err != nil || !state.needsWrite() {
		return err
	}

	if state.exists {
		// Never probe the legacy file for a value bd could not keep. Return
		// the error instead of attempting the write: a write that succeeded
		// after this check failed would skip the carry-over.
		if err := config.ConfigYamlReplaceable(path); err != nil {
			return fmt.Errorf("ensure user config: %s is not writable: %w", path, err)
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure user config: mkdir %s: %w", filepath.Dir(path), err)
	}

	if MacOSFamilyPlatform() {
		// Creating the lock file also proves the directory takes new files,
		// which is all the new-file path needs. An existing config is locked
		// beside its symlink-resolved target, the directory the check above
		// proved writable.
		lockPath := path
		if state.exists {
			if target, err := filepath.EvalSymlinks(path); err == nil {
				lockPath = target
			}
		}
		unlock, err := lockUserConfig(lockPath)
		if err != nil {
			return fmt.Errorf("ensure user config: lock %s: %w", lockPath, err)
		}
		defer unlock()

		// Re-read under the lock: another bd may have finished while this
		// one waited, and then there is nothing left to do (and nothing to
		// probe).
		state, err = readMetricsKeyState(path)
		if err != nil || !state.needsWrite() {
			return err
		}
		// Never probe the legacy file for keys the existing config cannot
		// take: a top level that is not a mapping, or a metrics section that
		// already holds a scalar or a list, fails the write below every run,
		// so the probe would repeat every run too.
		if state.exists {
			if err := checkMetricsKeysWritable(path, state); err != nil {
				return err
			}
		}
	}
	if !state.exists {
		if _, lerr := os.Lstat(path); lerr == nil {
			return fmt.Errorf("ensure user config: %s is a dangling symlink", path)
		}
	}

	var carry metricsCarryOver
	if state.needDisabled {
		carry = legacyDarwinMetricsCarryOver()
	}

	if !state.exists {
		created, err := createUserConfig(path, carry.bootstrapBody())
		if err != nil {
			return err
		}
		if created {
			carry.announce()
			return nil
		}
		// A writer that does not take the lock (an older bd, or bd config
		// set) created the file meanwhile: fill in what it lacks.
		if state, err = readMetricsKeyState(path); err != nil || !state.needsWrite() {
			return err
		}
	}
	return writeMissingMetricsKeys(state, carry)
}

// metricsKeyState is what the user config at a path holds of the keys the
// bootstrap maintains.
type metricsKeyState struct {
	exists         bool
	needDisabled   bool
	needEndpoint   bool
	hasNoticeShown bool
}

func (s metricsKeyState) needsWrite() bool { return s.needDisabled || s.needEndpoint }

func readMetricsKeyState(path string) (metricsKeyState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a validated absolute user config path
	if errors.Is(err, fs.ErrNotExist) {
		return metricsKeyState{needDisabled: true, needEndpoint: true}, nil
	}
	if err != nil {
		return metricsKeyState{}, fmt.Errorf("ensure user config: read %s: %w", path, err)
	}
	state := metricsKeyState{exists: true}
	if commentedMetricsRe.Match(data) {
		return state, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return metricsKeyState{}, fmt.Errorf("ensure user config: parse %s: %w", path, err)
	}
	state.needDisabled = !userConfigHasLeaf(&root, "metrics", "disabled")
	state.needEndpoint = !userConfigHasLeaf(&root, "metrics", "endpoint")
	state.hasNoticeShown = userConfigHasLeaf(&root, "metrics", "notice_shown")
	return state, nil
}

// checkMetricsKeysWritable runs writeMissingMetricsKeys' metrics.disabled and
// metrics.endpoint updates against the config at path in memory, with the
// writer's own update code (config.CheckYamlConfigUpdates), and returns the
// error the real write would hit. The values are placeholders: a carried
// value has the same YAML shape, so only the document decides the outcome.
func checkMetricsKeysWritable(path string, state metricsKeyState) error {
	data, err := os.ReadFile(path) //nolint:gosec // path is a validated absolute user config path
	if err != nil {
		return fmt.Errorf("ensure user config: read %s: %w", path, err)
	}
	var updates []config.YamlKeyValue
	if state.needDisabled {
		updates = append(updates, config.YamlKeyValue{Key: "metrics.disabled", Value: "false"})
	}
	if state.needEndpoint {
		updates = append(updates, config.YamlKeyValue{Key: "metrics.endpoint", Value: DefaultEndpoint})
	}
	if err := config.CheckYamlConfigUpdates(data, updates...); err != nil {
		return fmt.Errorf("ensure user config: %s cannot take the metrics keys: %w", path, err)
	}
	return nil
}

// writeMissingMetricsKeys fills the missing keys of an existing user config.
// carry is only consulted for metrics.disabled's companions when
// metrics.disabled itself is missing; it is the zero value otherwise.
func writeMissingMetricsKeys(state metricsKeyState, carry metricsCarryOver) error {
	if state.needDisabled {
		if err := config.SetUserYamlConfig("metrics.disabled", carry.disabledValue()); err != nil {
			return fmt.Errorf("ensure user config: set metrics.disabled: %w", err)
		}
		// The opt-out the notice describes is now on disk, whatever happens
		// to the companion keys below.
		carry.announce()
	}
	if state.needEndpoint {
		if err := config.SetUserYamlConfig("metrics.endpoint", carry.endpointValue()); err != nil {
			return fmt.Errorf("ensure user config: set metrics.endpoint: %w", err)
		}
	}
	if carry.noticeShown && !state.hasNoticeShown {
		if err := config.SetUserYamlConfig("metrics.notice_shown", "true"); err != nil {
			return fmt.Errorf("ensure user config: set metrics.notice_shown: %w", err)
		}
	}
	return nil
}

// lockUserConfig takes an exclusive lock on <path>.lock, serializing the
// bootstraps of concurrent bd processes (and goroutines: the lock belongs to
// the open file). The lock file is left in place:
// removing it after unlock would let two processes lock different inodes.
func lockUserConfig(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is from config.UserConfigYamlPath
	if err != nil {
		return nil, err
	}
	if err := lockfile.FlockExclusiveBlocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	}, nil
}

// createUserConfig publishes body as a new user config without ever exposing
// a partly written file: it writes a temp file in the same directory and
// hard-links it into place, which fails rather than replaces when the path
// already exists. created is false, with no error, when another writer got
// there first. On a filesystem without hard links it falls back to an
// exclusive create, the way bd created this file before GH#8113. That
// publishes the name before the content is written, so a bd reading the file
// without the lock (any concurrent run off macOS, or the pre-lock check on
// macOS) can see it partly written there.
func createUserConfig(path string, body []byte) (created bool, err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".~"+filepath.Base(path)+".bootstrap.") // mode 0600
	if err != nil {
		return false, fmt.Errorf("ensure user config: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_, werr := tmp.Write(body)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("ensure user config: write %s: %w", tmpName, werr)
	}

	linkErr := os.Link(tmpName, path)
	if linkErr == nil {
		return true, nil
	}
	if errors.Is(linkErr, fs.ErrExist) {
		return false, nil
	}
	return createUserConfigExclusive(path, body)
}

func createUserConfigExclusive(path string, body []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is from config.UserConfigYamlPath
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ensure user config: create %s: %w", path, err)
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("ensure user config: write %s: %w", path, werr)
	}
	return true, nil
}

// bootstrapGOOS and readLegacyDarwinUserConfig are the seams behind the
// darwin carry-over; tests inject another GOOS, count reads, and inject read
// errors. legacyCarryOverNotice, when non-nil, receives the fail-closed
// notice instead of the os.Stderr current at the time it is printed.
var (
	bootstrapGOOS              = runtime.GOOS
	readLegacyDarwinUserConfig = config.ReadLegacyDarwinUserConfigYaml
	legacyCarryOverNotice      io.Writer
)

// MacOSFamilyPlatform reports whether the bootstrap runs on darwin or ios
// (config.IsMacOSFamily), the platforms with the GH#8113 carry-over. Callers
// that make the post-bootstrap consent decision ask it too, so tests that
// install another GOOS with SetLegacyDarwinCarryOverForTest drive both.
func MacOSFamilyPlatform() bool { return config.IsMacOSFamily(bootstrapGOOS) }

// SetLegacyDarwinCarryOverForTest makes the bootstrap behave as if running on
// goos and, when read is non-nil, routes the legacy darwin config read through
// it (tests count probes and inject errors with it). It returns a restore
// function. Not safe for parallel tests.
func SetLegacyDarwinCarryOverForTest(goos string, read func(path string) ([]byte, error)) (restore func()) {
	prevGOOS, prevRead := bootstrapGOOS, readLegacyDarwinUserConfig
	bootstrapGOOS = goos
	if read != nil {
		readLegacyDarwinUserConfig = read
	}
	return func() { bootstrapGOOS, readLegacyDarwinUserConfig = prevGOOS, prevRead }
}

// metricsCarryOver holds the metrics choices found in the legacy darwin user
// config. The zero value means "nothing to carry": today's defaults.
type metricsCarryOver struct {
	disabled    *bool
	endpoint    string
	noticeShown bool

	// unreadablePath and unreadableErr are set when the legacy file exists
	// but could not be read or parsed. The carry-over then fails closed:
	// disabled is true and nothing else is carried.
	unreadablePath string
	unreadableErr  error
}

func (c metricsCarryOver) isZero() bool {
	return c.disabled == nil && c.endpoint == "" && !c.noticeShown
}

func (c metricsCarryOver) disabledValue() string {
	if c.disabled != nil {
		return strconv.FormatBool(*c.disabled)
	}
	return "false"
}

func (c metricsCarryOver) endpointValue() string {
	if c.endpoint != "" {
		return c.endpoint
	}
	return DefaultEndpoint
}

// bootstrapBody returns the new user config.yaml. With nothing to carry it is
// byte-for-byte the historical bootstrap body; otherwise it is YAML-encoded
// (so any endpoint is quoted safely) with the same 2-space indentation.
func (c metricsCarryOver) bootstrapBody() []byte {
	if c.isZero() {
		return []byte("metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n")
	}
	type metricsBlock struct {
		Disabled    bool   `yaml:"disabled"`
		Endpoint    string `yaml:"endpoint"`
		NoticeShown bool   `yaml:"notice_shown,omitempty"`
	}
	doc := struct {
		Metrics metricsBlock `yaml:"metrics"`
	}{metricsBlock{Disabled: c.disabled != nil && *c.disabled, Endpoint: c.endpointValue(), NoticeShown: c.noticeShown}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	// Encoding a struct of a bool and strings cannot fail.
	_ = enc.Encode(doc)
	_ = enc.Close()
	return buf.Bytes()
}

// announce prints the one-line fail-closed notice, once the opt-out it
// describes has been written.
func (c metricsCarryOver) announce() {
	if c.unreadableErr == nil {
		return
	}
	reasonErr := c.unreadableErr
	var pathErr *fs.PathError
	if errors.As(reasonErr, &pathErr) {
		reasonErr = pathErr.Err
	}
	// One line, even for a multi-line YAML error.
	reason := strings.Join(strings.Fields(reasonErr.Error()), " ")
	var w io.Writer = os.Stderr
	if legacyCarryOverNotice != nil {
		w = legacyCarryOverNotice
	}
	_, _ = fmt.Fprintf(w,
		"bd: metrics were left off because the old config at %s could not be read (%s); run \"bd metrics on\" to re-enable them\n",
		c.unreadablePath, reason)
}

// legacyDarwinMetricsCarryOver reads the pre-GH#8113 macOS user config
// (~/Library/Application Support/bd/config.yaml) and returns its
// metrics.disabled, metrics.endpoint and metrics.notice_shown, and nothing
// else. It runs only on darwin and ios, only from the bootstrap, and only when
// metrics.disabled is about to be written, so the folder macOS can guard with
// a privacy prompt is touched once per user config.
//
// Only a missing file (or no resolvable home, under which bd never read that
// file either) means "use today's defaults". A file that exists but cannot be
// read (EPERM after a privacy-prompt denial, EACCES, not a regular file, over
// the size cap, an I/O error) or does not parse as a YAML mapping fails
// closed: metrics.disabled true, nothing else carried, and a notice.
func legacyDarwinMetricsCarryOver() metricsCarryOver {
	if !MacOSFamilyPlatform() {
		return metricsCarryOver{}
	}
	path, err := config.LegacyDarwinUserConfigYamlPath()
	if err != nil {
		return metricsCarryOver{}
	}
	data, err := readLegacyDarwinUserConfig(path)
	if errors.Is(err, fs.ErrNotExist) {
		return metricsCarryOver{}
	}
	if err != nil {
		return failClosedCarryOver(path, err)
	}
	disabledRaw, disabledSet, err := config.YamlValueFromBytes(data, "metrics.disabled")
	if err != nil {
		return failClosedCarryOver(path, err)
	}
	var carry metricsCarryOver
	if disabledSet {
		if disabled, ok := config.ParseUserConfigBool(disabledRaw); ok {
			carry.disabled = &disabled
		}
	}
	if raw, ok, _ := config.YamlValueFromBytes(data, "metrics.endpoint"); ok {
		carry.endpoint = strings.TrimSpace(raw)
	}
	if raw, ok, _ := config.YamlValueFromBytes(data, "metrics.notice_shown"); ok {
		carry.noticeShown, _ = config.ParseUserConfigBool(raw)
	}
	return carry
}

func failClosedCarryOver(path string, err error) metricsCarryOver {
	disabled := true
	return metricsCarryOver{disabled: &disabled, unreadablePath: path, unreadableErr: err}
}

func userConfigHasLeaf(root *yaml.Node, parts ...string) bool {
	if root == nil || len(root.Content) == 0 {
		return false
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return false
	}

	flatKey := strings.Join(parts, ".")
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == flatKey {
			v := mapping.Content[i+1]
			return v.Kind == yaml.ScalarNode && v.Value != ""
		}
	}

	current := mapping
	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return false
		}
		idx := -1
		for i := 0; i < len(current.Content); i += 2 {
			k := current.Content[i]
			if k.Kind == yaml.ScalarNode && k.Value == part {
				idx = i
				break
			}
		}
		if idx == -1 {
			return false
		}
		current = current.Content[idx+1]
	}
	return current.Kind == yaml.ScalarNode && current.Value != ""
}
