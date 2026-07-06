package metrics

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
)

var commentedMetricsRe = regexp.MustCompile(`(?m)^\s*#\s*metrics\s*:`)

func EnsureUserConfigDefaults() error {
	path := config.UserConfigYamlPath()

	data, err := os.ReadFile(path) //nolint:gosec // path comes from config.UserConfigYamlPath
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

func writeUserConfigBootstrap(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure user config: mkdir %s: %w", filepath.Dir(path), err)
	}
	body := []byte("metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n")
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
