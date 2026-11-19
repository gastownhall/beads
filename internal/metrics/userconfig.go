package metrics

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
)

var commentedMetricsRe = regexp.MustCompile(`(?m)^\s*#\s*metrics\s*:`)

func EnsureUserConfigDefaults() error {
	path, err := config.UserConfigYamlPath()
	if err != nil {
		return fmt.Errorf("ensure user config: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is a validated absolute user config path
	if errors.Is(err, fs.ErrNotExist) {
		if err := writeUserConfigBootstrap(path); err != nil {
			return err
		}
		// Read back what was published: this run's bootstrap, or the file a
		// concurrent bd process created first. The defaults below then fill
		// whatever it lacks. This is a single re-read, never a retry of the
		// create, so a path that keeps reading as missing (a dangling
		// symlink) cannot loop.
		data, err = os.ReadFile(path) //nolint:gosec // path is a validated absolute user config path
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("ensure user config: %s is a dangling symlink", path)
		}
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

	if needDisabled {
		if err := config.SetUserYamlConfig("metrics.disabled", "false"); err != nil {
			return fmt.Errorf("ensure user config: set metrics.disabled: %w", err)
		}
	}
	if needEndpoint {
		if err := config.SetUserYamlConfig("metrics.endpoint", DefaultEndpoint); err != nil {
			return fmt.Errorf("ensure user config: set metrics.endpoint: %w", err)
		}
	}
	return nil
}

// writeUserConfigBootstrap creates the missing user config with metrics on and
// the default endpoint. A file that appeared since the caller's read (another
// bd process, or a dangling symlink) is left alone and reported as success:
// the caller re-reads the path once.
func writeUserConfigBootstrap(path string) error {
	body := []byte("metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n")
	err := config.CreateUserConfigYaml(path, body)
	if err == nil || errors.Is(err, config.ErrUserConfigExists) {
		return nil
	}
	return fmt.Errorf("ensure user config: %w", err)
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
