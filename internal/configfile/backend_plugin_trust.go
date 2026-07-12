package configfile

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bdconfig "github.com/steveyegge/beads/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	BackendPluginLocalConfigFileName = "config.local.yaml"
)

// BackendPluginConfig is the trusted, clone-local command declaration used to
// launch a backend plugin. Committed metadata.json may name the backend, but it
// must not authorize an executable.
type BackendPluginConfig struct {
	Backend string
	Command string
	Args    []string
	Source  string
}

// ResolveBackendPluginConfig resolves the executable for a plugin-backed store
// from trusted local sources only. Resolution order:
//   - BEADS_BACKEND_PLUGIN_COMMAND / BEADS_BACKEND_PLUGIN_ARGS
//   - .beads/config.local.yaml
//   - user-global config.yaml
func ResolveBackendPluginConfig(beadsDir, backend string) (*BackendPluginConfig, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" || backend == BackendDolt {
		return nil, nil
	}
	if command := strings.TrimSpace(os.Getenv("BEADS_BACKEND_PLUGIN_COMMAND")); command != "" {
		return &BackendPluginConfig{
			Backend: backend,
			Command: command,
			Args:    splitBackendPluginArgs(os.Getenv("BEADS_BACKEND_PLUGIN_ARGS")),
			Source:  "BEADS_BACKEND_PLUGIN_COMMAND",
		}, nil
	}
	if cfg, ok, err := readBackendPluginConfigFile(filepath.Join(beadsDir, BackendPluginLocalConfigFileName), backend); err != nil {
		return nil, err
	} else if ok {
		return cfg, nil
	}
	if cfg, ok, err := readBackendPluginConfigFile(bdconfig.UserConfigYamlPath(), backend); err != nil {
		return nil, err
	} else if ok {
		return cfg, nil
	}
	return nil, nil
}

func readBackendPluginConfigFile(path, backend string) (*BackendPluginConfig, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the explicit local/user config path.
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading backend plugin config %s: %w", path, err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parsing backend plugin config %s: %w", path, err)
	}
	prefix := "backend_plugins." + backend + "."
	command, ok := yamlLookupString(root, prefix+"command")
	if !ok || strings.TrimSpace(command) == "" {
		return nil, false, nil
	}
	args := yamlLookupStringSlice(root, prefix+"args")
	return &BackendPluginConfig{
		Backend: backend,
		Command: strings.TrimSpace(command),
		Args:    args,
		Source:  path,
	}, true, nil
}

func yamlLookupString(root map[string]interface{}, key string) (string, bool) {
	if raw, ok := root[key]; ok {
		return yamlScalarString(raw)
	}
	var node interface{} = root
	for _, part := range strings.Split(key, ".") {
		m, ok := node.(map[string]interface{})
		if !ok {
			return "", false
		}
		node, ok = m[part]
		if !ok {
			return "", false
		}
	}
	return yamlScalarString(node)
}

func yamlLookupStringSlice(root map[string]interface{}, key string) []string {
	if raw, ok := root[key]; ok {
		return yamlStringSlice(raw)
	}
	var node interface{} = root
	for _, part := range strings.Split(key, ".") {
		m, ok := node.(map[string]interface{})
		if !ok {
			return nil
		}
		node, ok = m[part]
		if !ok {
			return nil
		}
	}
	return yamlStringSlice(node)
}

func yamlScalarString(v interface{}) (string, bool) {
	switch value := v.(type) {
	case nil:
		return "", false
	case string:
		return value, true
	default:
		return fmt.Sprintf("%v", value), true
	}
}

func yamlStringSlice(v interface{}) []string {
	switch value := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			s, ok := yamlScalarString(item)
			if ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if strings.TrimSpace(item) != "" {
				out = append(out, strings.TrimSpace(item))
			}
		}
		return out
	case string:
		return splitBackendPluginArgs(value)
	default:
		if s, ok := yamlScalarString(value); ok {
			return splitBackendPluginArgs(s)
		}
		return nil
	}
}

func splitBackendPluginArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	r := csv.NewReader(strings.NewReader(raw))
	r.Comma = ' '
	r.TrimLeadingSpace = true
	fields, err := r.Read()
	if err != nil {
		return strings.Fields(raw)
	}
	out := fields[:0]
	for _, field := range fields {
		if strings.TrimSpace(field) != "" {
			out = append(out, field)
		}
	}
	return out
}
