package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/yamlshim"
)

// ReposConfig represents the repos section of config.yaml
type ReposConfig struct {
	Primary    string   `yaml:"primary,omitempty"`
	Additional []string `yaml:"additional,omitempty,flow"`
}

// configFile represents the structure for reading/writing config.yaml
type configFile struct {
	root yamlshim.Node
}

// FindConfigYAMLPath finds the config.yaml file in .beads directory
// Walks up from CWD to find .beads/config.yaml
func FindConfigYAMLPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		beadsDir := filepath.Join(dir, ".beads")
		configPath := filepath.Join(beadsDir, "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
	}

	return "", fmt.Errorf("no .beads/config.yaml found in current directory or parents")
}

// GetReposFromYAML reads the repos configuration from config.yaml
// Returns an empty ReposConfig if repos section doesn't exist
func GetReposFromYAML(configPath string) (*ReposConfig, error) {
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from caller
	if err != nil {
		if os.IsNotExist(err) {
			return &ReposConfig{}, nil
		}
		return nil, fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Parse into a generic map to extract repos section
	var cfg map[string]interface{}
	if err := yamlshim.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml: %w", err)
	}

	repos := &ReposConfig{}
	if reposRaw, ok := cfg["repos"]; ok && reposRaw != nil {
		reposMap, ok := reposRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("repos section is not a map")
		}

		if primary, ok := reposMap["primary"].(string); ok {
			repos.Primary = primary
		}

		if additional, ok := reposMap["additional"]; ok && additional != nil {
			switch v := additional.(type) {
			case []interface{}:
				for _, item := range v {
					if str, ok := item.(string); ok {
						repos.Additional = append(repos.Additional, str)
					}
				}
			}
		}
	}

	return repos, nil
}

// SetReposInYAML writes the repos configuration to config.yaml
// It preserves other config sections where possible
func SetReposInYAML(configPath string, repos *ReposConfig) error {
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from caller
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config.yaml: %w", err)
	}

	var root yamlshim.Node
	if len(data) > 0 {
		if err := yamlshim.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("failed to parse config.yaml: %w", err)
		}
	}

	if root.Kind != yamlshim.DocumentNode || len(root.Content) == 0 {
		root = yamlshim.Node{
			Kind: yamlshim.DocumentNode,
			Content: []*yamlshim.Node{
				{Kind: yamlshim.MappingNode},
			},
		}
	}

	mapping := root.Content[0]
	if mapping.Kind != yamlshim.MappingNode {
		root.Content[0] = &yamlshim.Node{Kind: yamlshim.MappingNode}
		mapping = root.Content[0]
	}

	reposIndex := -1
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "repos" {
			reposIndex = i
			break
		}
	}

	reposNode := buildReposNode(repos)

	if reposIndex >= 0 {
		if reposNode == nil {
			mapping.Content = append(mapping.Content[:reposIndex], mapping.Content[reposIndex+2:]...)
		} else {
			mapping.Content[reposIndex+1] = reposNode
		}
	} else if reposNode != nil {
		mapping.Content = append(mapping.Content,
			&yamlshim.Node{Kind: yamlshim.ScalarNode, Value: "repos"},
			reposNode,
		)
	}

	var buf bytes.Buffer
	encoder := yamlshim.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("failed to encode config.yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("failed to close encoder: %w", err)
	}

	if err := os.WriteFile(configPath, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	if v != nil {
		if err := v.ReadInConfig(); err != nil {
			_ = err
		}
	}

	return nil
}

// buildReposNode creates a yamlshim.Node for the repos configuration
func buildReposNode(repos *ReposConfig) *yamlshim.Node {
	if repos == nil || (repos.Primary == "" && len(repos.Additional) == 0) {
		return nil
	}

	node := &yamlshim.Node{Kind: yamlshim.MappingNode}

	if repos.Primary != "" {
		node.Content = append(node.Content,
			&yamlshim.Node{Kind: yamlshim.ScalarNode, Value: "primary"},
			&yamlshim.Node{Kind: yamlshim.ScalarNode, Value: repos.Primary, Style: yamlshim.DoubleQuotedStyle},
		)
	}

	if len(repos.Additional) > 0 {
		additionalNode := &yamlshim.Node{Kind: yamlshim.SequenceNode}
		for _, path := range repos.Additional {
			additionalNode.Content = append(additionalNode.Content,
				&yamlshim.Node{Kind: yamlshim.ScalarNode, Value: path, Style: yamlshim.DoubleQuotedStyle},
			)
		}
		node.Content = append(node.Content,
			&yamlshim.Node{Kind: yamlshim.ScalarNode, Value: "additional"},
			additionalNode,
		)
	}

	return node
}

// AddRepo adds a repository to the repos.additional list in config.yaml
// If primary is not set, it defaults to "."
func AddRepo(configPath, repoPath string) error {
	repos, err := GetReposFromYAML(configPath)
	if err != nil {
		return fmt.Errorf("failed to get repos config: %w", err)
	}

	if repos.Primary == "" {
		repos.Primary = "."
	}

	for _, existing := range repos.Additional {
		if existing == repoPath {
			return fmt.Errorf("repository already configured: %s", repoPath)
		}
	}

	repos.Additional = append(repos.Additional, repoPath)

	return SetReposInYAML(configPath, repos)
}

// RemoveRepo removes a repository from the repos.additional list in config.yaml
func RemoveRepo(configPath, repoPath string) error {
	repos, err := GetReposFromYAML(configPath)
	if err != nil {
		return fmt.Errorf("failed to get repos config: %w", err)
	}

	found := false
	newAdditional := make([]string, 0, len(repos.Additional))
	for _, existing := range repos.Additional {
		if existing == repoPath {
			found = true
			continue
		}
		newAdditional = append(newAdditional, existing)
	}

	if !found {
		return fmt.Errorf("repository not found: %s", repoPath)
	}

	repos.Additional = newAdditional

	if len(repos.Additional) == 0 {
		repos.Primary = ""
	}

	return SetReposInYAML(configPath, repos)
}

// ListRepos returns the current repos configuration from YAML
func ListRepos(configPath string) (*ReposConfig, error) {
	return GetReposFromYAML(configPath)
}
