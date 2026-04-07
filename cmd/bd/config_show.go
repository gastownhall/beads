package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// configEntry represents a single configuration key with its effective value and source.
type configEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show all effective configuration with provenance",
	Long: `Display a unified view of all effective configuration across all sources
with annotations showing where each value comes from.

Sources (by precedence for Viper-managed keys):
  - env          Environment variable (BD_* or BEADS_*)
  - config.yaml  Project config file (.beads/config.yaml)
  - default      Built-in default value

Additional sources:
  - metadata     Connection settings from .beads/metadata.json
  - database     Integration config stored in the Dolt database
  - git          Git config (e.g., beads.role)

Examples:
  bd config show
  bd config show --json
  bd config show --source config.yaml`,
	Run: func(cmd *cobra.Command, _ []string) {
		sourceFilter, _ := cmd.Flags().GetString("source")

		entries := collectConfigEntries()

		if sourceFilter != "" {
			entries = filterBySource(entries, sourceFilter)
		}

		if jsonOutput {
			outputJSON(entries)
			return
		}

		if len(entries) == 0 {
			fmt.Println("No configuration found")
			return
		}

		printConfigEntries(entries)
	},
}

func init() {
	configShowCmd.Flags().String("source", "", "Filter by source (e.g., config.yaml, env, default, metadata, database, git)")
	configCmd.AddCommand(configShowCmd)
}

// collectConfigEntries gathers configuration from all sources into a unified list.
func collectConfigEntries() []configEntry {
	var entries []configEntry

	// 1. Viper-managed keys (config.yaml + defaults + env vars)
	entries = append(entries, collectViperEntries()...)

	// 2. metadata.json fields
	entries = append(entries, collectMetadataEntries()...)

	// 3. Database-stored config (best-effort)
	entries = append(entries, collectDatabaseEntries()...)

	// 4. Git config (beads.role)
	entries = append(entries, collectGitConfigEntries()...)

	// Sort by key for stable output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	return entries
}

// collectViperEntries returns entries from all Viper-registered keys.
// Skips keys with empty default values that haven't been explicitly set.
func collectViperEntries() []configEntry {
	var entries []configEntry

	keys := config.AllKeys()
	if keys == nil {
		return entries
	}

	// Track which keys we've already added to avoid duplicates
	seen := make(map[string]bool)

	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true

		// Skip map-type keys that Viper flattens into individual dotted paths.
		// These are container keys (directory.labels, external_projects, repos)
		// whose individual sub-keys are already included.
		if isContainerKey(key) {
			continue
		}

		value := formatViperValue(config.GetString(key))
		source := config.GetValueSource(key)

		sourceLabel := viperSourceLabel(key, source)

		// Skip empty defaults — they add noise without information
		if source == config.SourceDefault && value == "" {
			continue
		}

		entries = append(entries, configEntry{
			Key:    key,
			Value:  value,
			Source: sourceLabel,
		})
	}

	return entries
}

// isContainerKey returns true for keys that are map/struct containers
// (e.g., "directory.labels", "external_projects") rather than scalar values.
func isContainerKey(key string) bool {
	containers := []string{"directory.labels", "external_projects", "repos"}
	for _, c := range containers {
		if key == c {
			return true
		}
	}
	return false
}

// formatViperValue converts a Viper value to a display string.
func formatViperValue(value string) string {
	if value == "" {
		return ""
	}
	return value
}

// viperSourceLabel returns a human-readable source label for a Viper key.
func viperSourceLabel(key string, source config.ConfigSource) string {
	switch source {
	case config.SourceEnvVar:
		if envName := config.EnvVarName(key); envName != "" {
			return "env: " + envName
		}
		return "env"
	case config.SourceConfigFile:
		return "config.yaml"
	default:
		return "default"
	}
}

// collectMetadataEntries reads .beads/metadata.json and returns entries for its fields.
func collectMetadataEntries() []configEntry {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil
	}

	var entries []configEntry

	add := func(key, value string) {
		if value != "" {
			entries = append(entries, configEntry{Key: key, Value: value, Source: "metadata"})
		}
	}
	addInt := func(key string, value int) {
		if value != 0 {
			entries = append(entries, configEntry{Key: key, Value: fmt.Sprintf("%d", value), Source: "metadata"})
		}
	}
	addBool := func(key string, value bool) {
		if value {
			entries = append(entries, configEntry{Key: key, Value: "true", Source: "metadata"})
		}
	}

	add("dolt_mode", cfg.DoltMode)
	add("dolt_server_host", cfg.DoltServerHost)
	addInt("dolt_server_port", cfg.DoltServerPort)
	add("dolt_server_user", cfg.DoltServerUser)
	add("dolt_database", cfg.DoltDatabase)
	addBool("dolt_server_tls", cfg.DoltServerTLS)
	add("dolt_data_dir", cfg.DoltDataDir)
	addInt("dolt_remotesapi_port", cfg.DoltRemotesAPIPort)
	add("project_id", cfg.ProjectID)
	addInt("deletions_retention_days", cfg.DeletionsRetentionDays)
	addInt("stale_closed_issues_days", cfg.StaleClosedIssuesDays)

	return entries
}

// collectDatabaseEntries attempts to load database-stored config.
// Returns nil if the database is unavailable (graceful degradation).
func collectDatabaseEntries() []configEntry {
	if err := ensureDirectMode("config show"); err != nil {
		return nil
	}

	s := getStore()
	if s == nil {
		return nil
	}

	ctx := getRootContext()
	dbConfig, err := s.GetAllConfig(ctx)
	if err != nil {
		return nil
	}

	var entries []configEntry
	for key, value := range dbConfig {
		entries = append(entries, configEntry{Key: key, Value: value, Source: "database"})
	}

	return entries
}

// collectGitConfigEntries reads beads-related values from git config.
func collectGitConfigEntries() []configEntry {
	var entries []configEntry

	// beads.role is the only git config key currently
	cmd := exec.Command("git", "config", "--get", "beads.role")
	output, err := cmd.Output()
	if err == nil {
		value := strings.TrimSpace(string(output))
		if value != "" {
			entries = append(entries, configEntry{Key: "beads.role", Value: value, Source: "git"})
		}
	}

	return entries
}

// filterBySource returns only entries matching the given source prefix.
func filterBySource(entries []configEntry, source string) []configEntry {
	var filtered []configEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Source, source) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// printConfigEntries renders entries in human-readable format with aligned columns.
func printConfigEntries(entries []configEntry) {
	// Calculate alignment widths
	maxKeyLen := 0
	maxValueLen := 0
	for _, e := range entries {
		if len(e.Key) > maxKeyLen {
			maxKeyLen = len(e.Key)
		}
		if len(e.Value) > maxValueLen {
			maxValueLen = len(e.Value)
		}
	}

	// Cap value column width to avoid absurdly wide lines
	if maxValueLen > 60 {
		maxValueLen = 60
	}

	for _, e := range entries {
		displayValue := e.Value
		if len(displayValue) > 60 {
			displayValue = displayValue[:57] + "..."
		}
		fmt.Fprintf(os.Stdout, "  %-*s = %-*s  (%s)\n", maxKeyLen, e.Key, maxValueLen, displayValue, e.Source)
	}
}
