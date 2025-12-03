package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/steveyegge/beads/internal/debug"
)

var v *viper.Viper

// Initialize sets up the viper configuration singleton
// Should be called once at application startup
func Initialize() error {
	v = viper.New()

	// Set config type to yaml (we only load config.yaml, not config.json)
	v.SetConfigType("yaml")

	// Explicitly locate config.yaml and use SetConfigFile to avoid picking up config.json
	// Precedence: project .beads/config.yaml > ~/.config/bd/config.yaml > ~/.beads/config.yaml
	configFileSet := false

	// 1. Walk up from CWD to find project .beads/config.yaml
	//    This allows commands to work from subdirectories
	cwd, err := os.Getwd()
	if err == nil && !configFileSet {
		// Walk up parent directories to find .beads/config.yaml
		for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			beadsDir := filepath.Join(dir, ".beads")
			configPath := filepath.Join(beadsDir, "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				// Found .beads/config.yaml - set it explicitly
				v.SetConfigFile(configPath)
				configFileSet = true
				break
			}
		}
	}

	// 2. User config directory (~/.config/bd/config.yaml)
	if !configFileSet {
		if configDir, err := os.UserConfigDir(); err == nil {
			configPath := filepath.Join(configDir, "bd", "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				v.SetConfigFile(configPath)
				configFileSet = true
			}
		}
	}

	// 3. Home directory (~/.beads/config.yaml)
	if !configFileSet {
		if homeDir, err := os.UserHomeDir(); err == nil {
			configPath := filepath.Join(homeDir, ".beads", "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				v.SetConfigFile(configPath)
				configFileSet = true
			}
		}
	}

	// Automatic environment variable binding
	// Environment variables take precedence over config file
	// E.g., BD_JSON, BD_NO_DAEMON, BD_ACTOR, BD_DB
	v.SetEnvPrefix("BD")
	
	// Replace hyphens and dots with underscores for env var mapping
	// This allows BD_NO_DAEMON to map to "no-daemon" config key
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	// Set defaults for all flags
	v.SetDefault("json", false)
	v.SetDefault("no-daemon", false)
	v.SetDefault("no-auto-flush", false)
	v.SetDefault("no-auto-import", false)
	v.SetDefault("no-db", false)
	v.SetDefault("db", "")
	v.SetDefault("actor", "")
	v.SetDefault("issue-prefix", "")
	
	// Additional environment variables (not prefixed with BD_)
	// These are bound explicitly for backward compatibility
	_ = v.BindEnv("flush-debounce", "BEADS_FLUSH_DEBOUNCE")
	_ = v.BindEnv("auto-start-daemon", "BEADS_AUTO_START_DAEMON")
	
	// Set defaults for additional settings
	v.SetDefault("flush-debounce", "30s")
	v.SetDefault("auto-start-daemon", true)
	
	// Routing configuration defaults
	v.SetDefault("routing.mode", "auto")
	v.SetDefault("routing.default", ".")
	v.SetDefault("routing.maintainer", ".")
	v.SetDefault("routing.contributor", "~/.beads-planning")

	// Sync configuration defaults (bd-4u8)
	v.SetDefault("sync.require_confirmation_on_mass_delete", false)

	// Read config file if it was found
	if configFileSet {
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
		debug.Logf("Debug: loaded config from %s\n", v.ConfigFileUsed())
	} else {
		// No config.yaml found - use defaults and environment variables
		debug.Logf("Debug: no config.yaml found; using defaults and environment variables\n")
	}

	return nil
}

// GetString retrieves a string configuration value
func GetString(key string) string {
	if v == nil {
		return ""
	}
	return v.GetString(key)
}

// GetBool retrieves a boolean configuration value
func GetBool(key string) bool {
	if v == nil {
		return false
	}
	return v.GetBool(key)
}

// GetInt retrieves an integer configuration value
func GetInt(key string) int {
	if v == nil {
		return 0
	}
	return v.GetInt(key)
}

// GetDuration retrieves a duration configuration value
func GetDuration(key string) time.Duration {
	if v == nil {
		return 0
	}
	return v.GetDuration(key)
}

// Set sets a configuration value
func Set(key string, value interface{}) {
	if v != nil {
		v.Set(key, value)
	}
}

// BindPFlag is reserved for future use if we want to bind Cobra flags directly to Viper
// For now, we handle flag precedence manually in PersistentPreRun
// Uncomment and implement if needed:
//
// func BindPFlag(key string, flag *pflag.Flag) error {
// 	if v == nil {
// 		return fmt.Errorf("viper not initialized")
// 	}
// 	return v.BindPFlag(key, flag)
// }

// AllSettings returns all configuration settings as a map
func AllSettings() map[string]interface{} {
	if v == nil {
		return map[string]interface{}{}
	}
	return v.AllSettings()
}

// GetStringSlice retrieves a string slice configuration value
func GetStringSlice(key string) []string {
	if v == nil {
		return []string{}
	}
	return v.GetStringSlice(key)
}

// MultiRepoConfig contains configuration for multi-repo support
type MultiRepoConfig struct {
	Primary    string   // Primary repo path (where canonical issues live)
	Additional []string // Additional repos to hydrate from
}

// GetMultiRepoConfig retrieves multi-repo configuration
// Returns nil if multi-repo is not configured (single-repo mode)
func GetMultiRepoConfig() *MultiRepoConfig {
	if v == nil {
		return nil
	}
	
	// Check if repos.primary is set (indicates multi-repo mode)
	primary := v.GetString("repos.primary")
	if primary == "" {
		return nil // Single-repo mode
	}
	
	return &MultiRepoConfig{
		Primary:    primary,
		Additional: v.GetStringSlice("repos.additional"),
	}
}
