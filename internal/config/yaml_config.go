package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// YamlOnlyKeys are configuration keys that must be stored in config.yaml
// rather than the database. These are "startup" settings that are
// read before the database is opened.
//
// This fixes GH#536: users were confused when `bd config set no-db true`
// appeared to succeed but had no effect (because no-db is read from yaml
// at startup, not from the database).
var YamlOnlyKeys = map[string]bool{
	// Bootstrap flags (affect how bd starts)
	"no-db": true,
	"json":  true,

	// Database and identity
	"db":       true,
	"actor":    true,
	"identity": true,

	// Git settings
	"git.author":      true,
	"git.no-gpg-sign": true,
	"no-push":         true,
	"no-git-ops":      true, // Disable git ops in bd prime session close protocol (GH#593)

	// Sync settings
	"sync.remote":     true, // Primary: any Dolt-compatible remote URL
	"sync.git-remote": true, // Deprecated: falls back from sync.remote
	"sync.require_confirmation_on_mass_delete": true,

	// Routing settings
	"routing.mode":        true,
	"routing.default":     true,
	"routing.maintainer":  true,
	"routing.contributor": true,

	// Create command settings
	"create.require-description": true,

	// Validation settings (bd-t7jq)
	// Values: "warn" | "error" | "none"
	"validation.on-create": true,
	"validation.on-close":  true,
	"validation.on-sync":   true,

	// Hierarchy settings (GH#995)
	"hierarchy.max-depth": true,

	// Backup settings (must be in yaml so GetValueSource can detect overrides)
	"backup.enabled":  true,
	"backup.interval": true,
	"backup.git-push": true,
	"backup.git-repo": true,

	// Dolt server settings
	"dolt.idle-timeout":  true, // Idle auto-stop timeout (default "30m", "0" disables)
	"dolt.shared-server": true, // Shared Dolt server at ~/.beads/shared-server/ (GH#2377)
	"dolt.max-conns":     true, // Connection pool size override (default 10, GH#3140)

	// Secrets: tokens and API keys must NOT be stored in the Dolt database
	// because that data is pushed to remotes, triggering secret-scanning
	// blocks on GitHub. Store them in local config.yaml instead.
	"github.token":   true,
	"linear.api_key": true,
}

// IsYamlOnlyKey returns true if the given key should be stored in config.yaml
// rather than the Dolt database.
func IsYamlOnlyKey(key string) bool {
	// Check exact match
	if YamlOnlyKeys[key] {
		return true
	}

	// Check prefix matches for nested keys
	prefixes := []string{"routing.", "sync.", "git.", "directory.", "repos.", "external_projects.", "validation.", "hierarchy.", "ai.", "backup.", "export.", "dolt.", "federation."}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// secretKeyPatterns are substrings that identify a yaml-only key as containing
// sensitive material that should not be written to git-tracked files.
var secretKeyPatterns = []string{"api_key", "api-key", "secret", "token", "password"}

// IsSecretKey returns true if the given config key holds sensitive material
// (API keys, tokens, passwords) that should not be committed to git.
func IsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, pattern := range secretKeyPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// isGitTracked returns true if the file at path is tracked by git
// (i.e., has been git-added). Uses `git ls-files --error-unmatch`.
func isGitTracked(path string) bool {
	cmd := exec.Command("git", "ls-files", "--error-unmatch", path)
	cmd.Dir = filepath.Dir(path)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

var secretKeyEnvVarHints = map[string]string{ //nolint:gosec // Values are environment variable names, not credentials.
	"ai.api_key":     "ANTHROPIC_API_KEY",
	"github.token":   "GITHUB_TOKEN",
	"linear.api_key": "LINEAR_API_KEY",
}

// secretKeyEnvVarHint returns a suggested environment variable name for a
// secret config key, e.g. "linear.api_key" -> "LINEAR_API_KEY".
func secretKeyEnvVarHint(key string) string {
	if envVar, ok := secretKeyEnvVarHints[key]; ok {
		return envVar
	}
	return "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
}

// CheckSecretKeyGitSafety checks whether writing key to the project's
// config.yaml would expose a secret in git history. Returns a descriptive
// error with remediation steps if so; nil otherwise. Non-secret keys always
// return nil.
func CheckSecretKeyGitSafety(key string) error {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return nil // can't resolve path; let the write fail with its own error
	}
	return checkSecretGitTracked(configPath, key)
}

func checkSecretGitTracked(configPath, key string) error {
	if !IsYamlOnlyKey(key) {
		return nil
	}
	if !IsSecretKey(key) {
		return nil
	}
	if !isGitTracked(configPath) {
		return nil
	}
	envVar := secretKeyEnvVarHint(key)
	return fmt.Errorf(
		"refusing to write secret key %q to git-tracked config file %s\n\n"+
			"This would expose your secret in git history. Instead:\n"+
			"  export %s=\"your-key-here\"    # add to ~/.secrets or ~/.zshrc\n\n"+
			"Or move config.yaml out of git tracking:\n"+
			"  git rm --cached %s\n"+
			"  echo \"config.yaml\" >> %s/.gitignore\n\n"+
			"To override this check (e.g., for testing):\n"+
			"  bd config set --force-git-tracked %s \"value\"",
		key, configPath,
		envVar,
		configPath,
		filepath.Dir(configPath),
		key,
	)
}

// keyAliases maps alternative key names to their canonical yaml form.
// This ensures consistency when users use different formats (dot vs hyphen).
var keyAliases = map[string]string{}

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
	// Validate specific keys (GH#995)
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}

	configPath, err := findProjectConfigYaml()
	if err != nil {
		return err
	}

	return setYamlConfigAtPath(configPath, key, value)
}

// SetYamlConfigInDir sets a configuration value in the config.yaml located in
// the provided beadsDir, bypassing CWD/worktree discovery. Use this when the
// caller has already resolved the authoritative workspace and needs to avoid
// local worktree stubs shadowing the real shared config location.
func SetYamlConfigInDir(beadsDir, key, value string) error {
	// Validate specific keys (GH#995)
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no config.yaml found in %s (run 'bd init' first)", beadsDir)
		}
		return fmt.Errorf("failed to stat config.yaml: %w", err)
	}

	return setYamlConfigAtPath(configPath, key, value)
}

func setYamlConfigAtPath(configPath, key, value string) error {

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
// Keys are normalized to their canonical yaml format (e.g., sync.branch -> sync-branch).
func GetYamlConfig(key string) string {
	if v == nil {
		return ""
	}
	normalizedKey := normalizeYamlKey(key)
	return v.GetString(normalizedKey)
}

// UnsetYamlConfig removes a configuration value from the project's config.yaml file.
// The key line is commented out (prefixed with "# ") to preserve it as documentation.
func UnsetYamlConfig(key string) error {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return err
	}

	normalizedKey := normalizeYamlKey(key)

	content, err := os.ReadFile(configPath) //nolint:gosec // configPath is from findProjectConfigYaml
	if err != nil {
		return fmt.Errorf("failed to read config.yaml: %w", err)
	}

	newContent := commentOutYamlKey(string(content), normalizedKey)

	if err := os.WriteFile(configPath, []byte(newContent), 0600); err != nil { //nolint:gosec // configPath is validated
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

// findProjectConfigYaml finds the active config.yaml path for YAML-only config writes.
//
// Resolution order:
//  1. BEADS_DIR/config.yaml (when BEADS_DIR is set)
//  2. Walk up from CWD to find .beads/config.yaml
//
// This keeps YAML-only config behavior aligned with runtime resolution when
// BEADS_DIR points to an external runtime directory.
func findProjectConfigYaml() (string, error) {
	return findProjectConfigYamlWithFinder(findProjectBeadsDir)
}

func findProjectConfigYamlWithFinder(findBeadsDir func() string) (string, error) {
	// Respect BEADS_DIR first when set.
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		configPath := filepath.Join(beadsDir, "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
		return "", fmt.Errorf("no config.yaml found in BEADS_DIR (%s) (run 'bd init' first)", beadsDir)
	}

	if configPath := projectConfigPathFromLoadedState(); configPath != "" {
		return configPath, nil
	}

	if findBeadsDir != nil {
		if beadsDir := findBeadsDir(); beadsDir != "" {
			configPath := filepath.Join(beadsDir, "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				return configPath, nil
			}
		}
	}

	return "", fmt.Errorf("no .beads/config.yaml found (run 'bd init' first)")
}

func projectConfigPathFromLoadedState() string {
	configPath := ConfigFileUsed()
	if configPath == "" {
		return ""
	}
	if filepath.Base(configPath) != "config.yaml" {
		return ""
	}
	if filepath.Base(filepath.Dir(configPath)) != ".beads" {
		return ""
	}
	if _, err := os.Stat(configPath); err != nil {
		return ""
	}
	return configPath
}

func findProjectBeadsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}
	}

	configPath := worktreeFallbackConfigPath(cwd)
	if configPath == "" {
		return ""
	}

	return filepath.Dir(configPath)
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

// commentOutYamlKey comments out a key in yaml content by prefixing the line with "# ".
// If the key is already commented or not found, the content is returned unchanged.
func commentOutYamlKey(content, key string) string {
	// Match the key line (not already commented)
	keyPattern := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*:`)

	var result []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if keyPattern.MatchString(line) {
			matches := keyPattern.FindStringSubmatch(line)
			indent := ""
			if len(matches) > 1 {
				indent = matches[1]
			}
			// Comment out the line, preserving indentation
			result = append(result, indent+"# "+strings.TrimLeft(line, " \t"))
		} else {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
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

// validateYamlConfigValue validates a configuration value before setting.
// Returns an error if the value is invalid for the given key.
func validateYamlConfigValue(key, value string) error {
	switch key {
	case "hierarchy.max-depth":
		// Must be a positive integer >= 1 (GH#995)
		depth, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("hierarchy.max-depth must be a positive integer, got %q", value)
		}
		if depth < 1 {
			return fmt.Errorf("hierarchy.max-depth must be at least 1, got %d", depth)
		}
	case "dolt.idle-timeout":
		// "0" disables, otherwise must be a valid Go duration
		if value != "0" {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("dolt.idle-timeout must be a duration (e.g. \"30m\", \"1h\") or \"0\" to disable, got %q", value)
			}
		}
	case "dolt.shared-server":
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" {
			return fmt.Errorf("dolt.shared-server must be \"true\" or \"false\", got %q", value)
		}
	}
	return nil
}
