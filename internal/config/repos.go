package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReposConfig represents the repos section of config.yaml
type ReposConfig struct {
	Primary    string   `yaml:"primary,omitempty"`
	Additional []string `yaml:"additional,omitempty,flow"`
}

// configFile represents the structure for reading/writing config.yaml
// We use yaml.Node to preserve comments and formatting
type configFile struct {
	root yaml.Node
}

// FindConfigYAMLPath finds the config.yaml file in .beads directory
// Walks up from CWD to find .beads/config.yaml
func FindConfigYAMLPath() (string, error) {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return "", fmt.Errorf("no .beads/config.yaml found in current directory or parents")
	}
	return configPath, nil
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
// It preserves other config sections and comments where possible
func SetReposInYAML(configPath string, repos *ReposConfig) error {
	// Read existing config or create new
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from caller
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Parse existing config into yaml.Node to preserve structure
	var root yaml.Node
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("failed to parse config.yaml: %w", err)
		}
	}

	// Handle empty or comment-only files by creating a valid document structure
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		root = yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{Kind: yaml.MappingNode},
			},
		}
	}

	// Get the mapping node (first content of document)
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		// If the document content isn't a mapping, replace it with one
		root.Content[0] = &yaml.Node{Kind: yaml.MappingNode}
		mapping = root.Content[0]
	}

	// Find or create repos section
	reposIndex := -1
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "repos" {
			reposIndex = i
			break
		}
	}

	// Build the repos node
	reposNode := buildReposNode(repos)

	if reposIndex >= 0 {
		// Update existing repos section
		if reposNode == nil {
			// Remove repos section entirely if empty
			mapping.Content = append(mapping.Content[:reposIndex], mapping.Content[reposIndex+2:]...)
		} else {
			mapping.Content[reposIndex+1] = reposNode
		}
	} else if reposNode != nil {
		// Add new repos section at the end
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "repos"},
			reposNode,
		)
	}

	// Marshal back to YAML
	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("failed to encode config.yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("failed to close encoder: %w", err)
	}

	// Write back to file
	if err := os.WriteFile(configPath, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	// Reload viper config so changes take effect immediately
	if v != nil {
		if err := v.ReadInConfig(); err != nil {
			// Not fatal - config is on disk, will be picked up on next command
			_ = err // Best effort: viper reload failure is non-fatal since config was already written to disk
		}
	}

	return nil
}

// buildReposNode creates a yaml.Node for the repos configuration
// Returns nil if repos is empty (no primary and no additional)
func buildReposNode(repos *ReposConfig) *yaml.Node {
	if repos == nil || (repos.Primary == "" && len(repos.Additional) == 0) {
		return nil
	}

	node := &yaml.Node{Kind: yaml.MappingNode}

	if repos.Primary != "" {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "primary"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: repos.Primary, Style: yaml.DoubleQuotedStyle},
		)
	}

	if len(repos.Additional) > 0 {
		additionalNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, path := range repos.Additional {
			additionalNode.Content = append(additionalNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: path, Style: yaml.DoubleQuotedStyle},
			)
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "additional"},
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

	// Set primary to "." if not already set (standard multi-repo convention)
	if repos.Primary == "" {
		repos.Primary = "."
	}

	// Check if repo already exists
	for _, existing := range repos.Additional {
		if existing == repoPath {
			return fmt.Errorf("repository already configured: %s", repoPath)
		}
	}

	// Add the new repo
	repos.Additional = append(repos.Additional, repoPath)

	return SetReposInYAML(configPath, repos)
}

// RemoveRepo removes a repository from the repos.additional list in config.yaml
func RemoveRepo(configPath, repoPath string) error {
	repos, err := GetReposFromYAML(configPath)
	if err != nil {
		return fmt.Errorf("failed to get repos config: %w", err)
	}

	// Find and remove the repo
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

	// If no repos left, clear primary too
	if len(repos.Additional) == 0 {
		repos.Primary = ""
	}

	return SetReposInYAML(configPath, repos)
}

// ListRepos returns the current repos configuration from YAML
func ListRepos(configPath string) (*ReposConfig, error) {
	return GetReposFromYAML(configPath)
}
