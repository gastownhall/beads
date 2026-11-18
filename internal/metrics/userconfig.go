package metrics

import (
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
)

var commentedMetricsRe = regexp.MustCompile(`(?m)^\s*#\s*metrics\s*:`)

// EnsureUserConfigDefaults writes metrics.disabled and metrics.endpoint into
// the user config bd reads when either is absent. On darwin, at that moment
// only, it carries a metrics choice over from the pre-GH#8113 user config in
// ~/Library/Application Support (see legacyDarwinMetricsCarryOver). Once both
// keys exist the function returns before any probe, so the legacy file is
// consulted at most once per machine.
func EnsureUserConfigDefaults() error {
	path, err := config.UserConfigYamlPath()
	if err != nil {
		return fmt.Errorf("ensure user config: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is a validated absolute user config path
	if errors.Is(err, fs.ErrNotExist) {
		return writeUserConfigBootstrap(path)
	}
	if err != nil {
		return fmt.Errorf("ensure user config: read %s: %w", path, err)
	}

	if commentedMetricsRe.Match(data) {
		return nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("ensure user config: parse %s: %w", path, err)
	}

	needDisabled := !userConfigHasLeaf(&root, "metrics", "disabled")
	needEndpoint := !userConfigHasLeaf(&root, "metrics", "endpoint")

	if !needDisabled && !needEndpoint {
		return nil
	}

	carry := legacyDarwinMetricsCarryOver()
	if needDisabled {
		if err := config.SetUserYamlConfig("metrics.disabled", carry.disabledValue()); err != nil {
			return fmt.Errorf("ensure user config: set metrics.disabled: %w", err)
		}
	}
	if needEndpoint {
		if err := config.SetUserYamlConfig("metrics.endpoint", carry.endpointValue()); err != nil {
			return fmt.Errorf("ensure user config: set metrics.endpoint: %w", err)
		}
	}
	if carry.noticeShown && !userConfigHasLeaf(&root, "metrics", "notice_shown") {
		if err := config.SetUserYamlConfig("metrics.notice_shown", "true"); err != nil {
			return fmt.Errorf("ensure user config: set metrics.notice_shown: %w", err)
		}
	}
	return nil
}

func writeUserConfigBootstrap(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure user config: mkdir %s: %w", filepath.Dir(path), err)
	}
	body, err := legacyDarwinMetricsCarryOver().bootstrapBody()
	if err != nil {
		return fmt.Errorf("ensure user config: encode %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is from config.UserConfigYamlPath
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return EnsureUserConfigDefaults()
		}
		return fmt.Errorf("ensure user config: create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("ensure user config: write %s: %w", path, err)
	}
	return nil
}

// bootstrapGOOS and readLegacyDarwinUserConfig are the seams behind the
// one-time darwin carry-over; tests inject another GOOS and count reads.
var (
	bootstrapGOOS              = runtime.GOOS
	readLegacyDarwinUserConfig = readRegularFileCapped
)

// SetLegacyDarwinCarryOverForTest makes the bootstrap behave as if running on
// goos and, when read is non-nil, routes the one legacy darwin config read
// through it (tests count probes with it). It returns a restore function. Not
// safe for parallel tests.
func SetLegacyDarwinCarryOverForTest(goos string, read func(path string) ([]byte, error)) (restore func()) {
	prevGOOS, prevRead := bootstrapGOOS, readLegacyDarwinUserConfig
	bootstrapGOOS = goos
	if read != nil {
		readLegacyDarwinUserConfig = read
	}
	return func() { bootstrapGOOS, readLegacyDarwinUserConfig = prevGOOS, prevRead }
}

// legacyDarwinUserConfigMaxBytes bounds the one legacy read; a real bd user
// config is a few hundred bytes.
const legacyDarwinUserConfigMaxBytes = 1 << 20

// metricsCarryOver holds the metrics choices found in the legacy darwin user
// config. The zero value means "nothing to carry": today's defaults.
type metricsCarryOver struct {
	disabled    *bool
	endpoint    string
	noticeShown bool
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
// byte-for-byte the historical bootstrap body.
func (c metricsCarryOver) bootstrapBody() ([]byte, error) {
	if c == (metricsCarryOver{}) {
		return []byte("metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n"), nil
	}
	type metricsBlock struct {
		Disabled    bool   `yaml:"disabled"`
		Endpoint    string `yaml:"endpoint"`
		NoticeShown bool   `yaml:"notice_shown,omitempty"`
	}
	disabled, _ := strconv.ParseBool(c.disabledValue())
	return yaml.Marshal(struct {
		Metrics metricsBlock `yaml:"metrics"`
	}{metricsBlock{Disabled: disabled, Endpoint: c.endpointValue(), NoticeShown: c.noticeShown}})
}

// legacyDarwinMetricsCarryOver reads the pre-GH#8113 macOS user config
// (~/Library/Application Support/bd/config.yaml) and returns its
// metrics.disabled, metrics.endpoint and metrics.notice_shown, and nothing
// else. It runs only on darwin and only from the bootstrap, at the moment the
// metrics keys are about to be written, so the folder macOS can guard with a
// privacy prompt is touched at most once per machine. A missing, unreadable or
// malformed file yields the zero value: today's defaults, silently.
func legacyDarwinMetricsCarryOver() metricsCarryOver {
	if bootstrapGOOS != "darwin" {
		return metricsCarryOver{}
	}
	path, err := config.LegacyDarwinUserConfigYamlPath()
	if err != nil {
		return metricsCarryOver{}
	}
	data, err := readLegacyDarwinUserConfig(path)
	if err != nil {
		return metricsCarryOver{}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return metricsCarryOver{}
	}
	var carry metricsCarryOver
	if raw, ok := userConfigLeaf(&root, "metrics", "disabled"); ok {
		if disabled, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			carry.disabled = &disabled
		}
	}
	if raw, ok := userConfigLeaf(&root, "metrics", "endpoint"); ok {
		carry.endpoint = strings.TrimSpace(raw)
	}
	if raw, ok := userConfigLeaf(&root, "metrics", "notice_shown"); ok {
		carry.noticeShown, _ = strconv.ParseBool(strings.TrimSpace(raw))
	}
	return carry
}

// readRegularFileCapped reads path only when it is a regular file (never a
// FIFO or device, which could block) of at most legacyDarwinUserConfigMaxBytes.
func readRegularFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is the fixed legacy darwin user config path
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > legacyDarwinUserConfigMaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, legacyDarwinUserConfigMaxBytes)
	}
	return io.ReadAll(io.LimitReader(f, legacyDarwinUserConfigMaxBytes+1))
}

func userConfigHasLeaf(root *yaml.Node, parts ...string) bool {
	value, ok := userConfigLeaf(root, parts...)
	return ok && value != ""
}

// userConfigLeaf returns the scalar at a dotted key, written either flat
// ("metrics.disabled: true") or nested, and whether a scalar is there.
func userConfigLeaf(root *yaml.Node, parts ...string) (string, bool) {
	if root == nil || len(root.Content) == 0 {
		return "", false
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return "", false
	}

	flatKey := strings.Join(parts, ".")
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == flatKey {
			v := mapping.Content[i+1]
			return v.Value, v.Kind == yaml.ScalarNode
		}
	}

	current := mapping
	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return "", false
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
			return "", false
		}
		current = current.Content[idx+1]
	}
	return current.Value, current.Kind == yaml.ScalarNode
}
