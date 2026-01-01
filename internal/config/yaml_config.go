package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// YamlOnlyKeys are configuration keys that must be stored in config.yaml
// rather than the SQLite database. These are "startup" settings that are
// read before the database is opened.
//
// This fixes GH#536: users were confused when `bd config set no-db true`
// appeared to succeed but had no effect (because no-db is read from yaml
// at startup, not from SQLite).
var YamlOnlyKeys = map[string]bool{
	// Bootstrap flags (affect how bd starts)
	"no-db":          true,
	"no-daemon":      true,
	"no-auto-flush":  true,
	"no-auto-import": true,
	"json":           true,
	"auto-start-daemon": true,

	// Database and identity
	"db":     true,
	"actor":  true,
	"identity": true,

	// Timing settings
	"flush-debounce":       true,
	"lock-timeout":         true,
	"remote-sync-interval": true,

	// Git settings
	"git.author":       true,
	"git.no-gpg-sign":  true,
	"no-push":    true,
	"no-git-ops": true, // Disable git ops in bd prime session close protocol (GH#593)

	// Sync settings
	"sync-branch":                           true,
	"sync.branch":                           true,
	"sync.require_confirmation_on_mass_delete": true,

	// Routing settings
	"routing.mode":        true,
	"routing.default":     true,
	"routing.maintainer":  true,
	"routing.contributor": true,

	// Create command settings
	"create.require-description": true,
}

// IsYamlOnlyKey returns true if the given key should be stored in config.yaml
// rather than the SQLite database.
func IsYamlOnlyKey(key string) bool {
	// Check exact match
	if YamlOnlyKeys[key] {
		return true
	}

	// Check prefix matches for nested keys
	prefixes := []string{"routing.", "sync.", "git.", "directory.", "repos.", "external_projects."}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// keyAliases maps alternative key names to their canonical yaml form.
// This ensures consistency when users use different formats (dot vs hyphen).
var keyAliases = map[string]string{
	"sync.branch": "sync-branch",
}

// normalizeYamlKey converts a key to its canonical yaml format.
// Some keys have aliases (e.g., sync.branch -> sync-branch) to handle
// different input formats consistently.
func normalizeYamlKey(key string) string {
	if canonical, ok := keyAliases[key]; ok {
		return canonical
	}
	return key
}

// SetYamlConfig sets a configuration value in the project's config.yaml file.
// It handles both adding new keys and updating existing (possibly commented) keys.
// Keys are normalized to their canonical yaml format (e.g., sync.branch -> sync-branch).
func SetYamlConfig(key, value string) error {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return err
	}

	// Normalize key to canonical yaml format
	normalizedKey := normalizeYamlKey(key)

	// Read existing config
	content, err := os.ReadFile(configPath) //nolint:gosec // configPath is from findProjectConfigYaml
	if err != nil {
		return fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Update or add the key
	newContent, err := updateYamlKey(string(content), normalizedKey, value)
	if err != nil {
		return err
	}

	// Write back
	if err := os.WriteFile(configPath, []byte(newContent), 0600); err != nil { //nolint:gosec // configPath is validated
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

// GetYamlConfig gets a configuration value from config.yaml.
// Returns empty string if key is not found or is commented out.
func GetYamlConfig(key string) string {
	if v == nil {
		return ""
	}
	return v.GetString(key)
}

// findProjectConfigYaml finds the project's .beads/config.yaml file.
func findProjectConfigYaml() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// Walk up parent directories to find .beads/config.yaml
	for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		configPath := filepath.Join(dir, ".beads", "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
	}

	return "", fmt.Errorf("no .beads/config.yaml found (run 'bd init' first)")
}

// updateYamlKey updates a key in yaml content, handling commented-out keys.
// If the key exists (commented or not), it updates it in place.
// If the key doesn't exist, it appends it at the end.
//
//nolint:unparam // error return kept for future validation
func updateYamlKey(content, key, value string) (string, error) {
	// Format the value appropriately
	formattedValue := formatYamlValue(value)
	newLine := fmt.Sprintf("%s: %s", key, formattedValue)

	// Build regex to match the key (commented or not)
	// Matches: "key: value" or "# key: value" with optional leading whitespace
	keyPattern := regexp.MustCompile(`^(\s*)(#\s*)?` + regexp.QuoteMeta(key) + `\s*:`)

	found := false
	var result []string

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if keyPattern.MatchString(line) {
			// Found the key - replace with new value (uncommented)
			// Preserve leading whitespace
			matches := keyPattern.FindStringSubmatch(line)
			indent := ""
			if len(matches) > 1 {
				indent = matches[1]
			}
			result = append(result, indent+newLine)
			found = true
		} else {
			result = append(result, line)
		}
	}

	if !found {
		// Key not found - append at end
		// Add blank line before if content doesn't end with one
		if len(result) > 0 && result[len(result)-1] != "" {
			result = append(result, "")
		}
		result = append(result, newLine)
	}

	return strings.Join(result, "\n"), nil
}

// formatYamlValue formats a value appropriately for YAML.
func formatYamlValue(value string) string {
	// Boolean values
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return lower
	}

	// Numeric values - return as-is
	if isNumeric(value) {
		return value
	}

	// Duration values (like "30s", "5m") - return as-is
	if isDuration(value) {
		return value
	}

	// For all other string-like values, quote to preserve YAML string semantics
	return fmt.Sprintf("%q", value)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isDuration(s string) bool {
	if len(s) < 2 {
		return false
	}
	suffix := s[len(s)-1]
	if suffix != 's' && suffix != 'm' && suffix != 'h' {
		return false
	}
	return isNumeric(s[:len(s)-1])
}

func needsQuoting(s string) bool {
	// Quote if contains special YAML characters
	special := []string{":", "#", "[", "]", "{", "}", ",", "&", "*", "!", "|", ">", "'", "\"", "%", "@", "`"}
	for _, c := range special {
		if strings.Contains(s, c) {
			return true
		}
	}
	// Quote if starts/ends with whitespace
	if strings.TrimSpace(s) != s {
		return true
	}
	return false
}
