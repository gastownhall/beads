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
		return writeUserConfigBootstrap(path)
	}
	if err != nil {
		return fmt.Errorf("ensure user config: read %s: %w", path, err)
	}

	return ensureExistingUserConfig(path, data)
}

// ensureExistingUserConfig is the existing-file half of
// EnsureUserConfigDefaults: honor a commented "metrics:" block, then fill
// missing metrics.disabled / metrics.endpoint leaves. It is also where the
// create-race re-read lands (see writeUserConfigBootstrap), so a peer's
// freshly published file gets exactly the same treatment as a file bd found.
func ensureExistingUserConfig(path string, data []byte) error {
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

// writeUserConfigBootstrap creates the missing user config and seeds the
// metrics block. Creation goes through config.CreateUserConfigYaml so the
// one-time macOS legacy carry-over (GH#8113) runs on the darwin create.
//
// The create-race bound: when the create reports "already exists", re-read
// once and treat the file as existing. A dangling symlink at the config path
// makes both the not-exist observation and the create's EEXIST true forever;
// the historical recursion into EnsureUserConfigDefaults from the EEXIST
// branch therefore recursed until the stack ran out. The re-read detects it
// (still not-exist) and reports it instead.
func writeUserConfigBootstrap(path string) error {
	body := []byte("metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n")
	err := config.CreateUserConfigYaml(path, body)
	if err == nil {
		return nil
	}
	if errors.Is(err, config.ErrUserConfigAlreadyExists) {
		data, rerr := os.ReadFile(path) //nolint:gosec // path is from config.UserConfigYamlPath
		if errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("ensure user config: %s is a dangling symlink", path)
		}
		if rerr != nil {
			return fmt.Errorf("ensure user config: read %s: %w", path, rerr)
		}
		return ensureExistingUserConfig(path, data)
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
