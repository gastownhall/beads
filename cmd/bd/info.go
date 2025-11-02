package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show database and daemon information",
	Long: `Display information about the current database path and daemon status.

This command helps debug issues where bd is using an unexpected database
or daemon connection. It shows:
  - The absolute path to the database file
  - Daemon connection status (daemon or direct mode)
  - If using daemon: socket path, health status, version
  - Database statistics (issue count)
  - Schema information (with --schema flag)

Examples:
  bd info
  bd info --json
  bd info --schema --json`,
	Run: func(cmd *cobra.Command, args []string) {
		schemaFlag, _ := cmd.Flags().GetBool("schema")

		// Get database path (absolute)
		absDBPath, err := filepath.Abs(dbPath)
		if err != nil {
			absDBPath = dbPath
		}

		// Build info structure
		info := map[string]interface{}{
			"database_path": absDBPath,
			"mode":          daemonStatus.Mode,
		}

		// Add daemon details if connected
		if daemonClient != nil {
			info["daemon_connected"] = true
			info["socket_path"] = daemonStatus.SocketPath

			// Get daemon health
			health, err := daemonClient.Health()
			if err == nil {
				info["daemon_version"] = health.Version
				info["daemon_status"] = health.Status
				info["daemon_compatible"] = health.Compatible
				info["daemon_uptime"] = health.Uptime
			}

			// Get issue count from daemon
			resp, err := daemonClient.Stats()
			if err == nil {
				var stats types.Statistics
				if jsonErr := json.Unmarshal(resp.Data, &stats); jsonErr == nil {
					info["issue_count"] = stats.TotalIssues
				}
			}
		} else {
			// Direct mode
			info["daemon_connected"] = false
			if daemonStatus.FallbackReason != "" && daemonStatus.FallbackReason != FallbackNone {
				info["daemon_fallback_reason"] = daemonStatus.FallbackReason
			}
			if daemonStatus.Detail != "" {
				info["daemon_detail"] = daemonStatus.Detail
			}

			// Get issue count from direct store
			if store != nil {
				ctx := context.Background()
				filter := types.IssueFilter{}
				issues, err := store.SearchIssues(ctx, "", filter)
				if err == nil {
					info["issue_count"] = len(issues)
				}
			}
		}

		// Add schema information if requested
		if schemaFlag && store != nil {
			ctx := context.Background()
			
			// Get schema version
			schemaVersion, err := store.GetMetadata(ctx, "bd_version")
			if err != nil {
				schemaVersion = "unknown"
			}
			
			// Get tables
			tables := []string{"issues", "dependencies", "labels", "config", "metadata"}
			
			// Get config
			configMap := make(map[string]string)
			prefix, _ := store.GetConfig(ctx, "issue_prefix")
			if prefix != "" {
				configMap["issue_prefix"] = prefix
			}
			
			// Get sample issue IDs
			filter := types.IssueFilter{}
			issues, err := store.SearchIssues(ctx, "", filter)
			sampleIDs := []string{}
			detectedPrefix := ""
			if err == nil && len(issues) > 0 {
				// Get first 3 issue IDs as samples
				maxSamples := 3
				if len(issues) < maxSamples {
					maxSamples = len(issues)
				}
				for i := 0; i < maxSamples; i++ {
					sampleIDs = append(sampleIDs, issues[i].ID)
				}
				// Detect prefix from first issue
				if len(issues) > 0 {
					detectedPrefix = extractPrefix(issues[0].ID)
				}
			}
			
			info["schema"] = map[string]interface{}{
				"tables":          tables,
				"schema_version":  schemaVersion,
				"config":          configMap,
				"sample_issue_ids": sampleIDs,
				"detected_prefix": detectedPrefix,
			}
		}

		// JSON output
		if jsonOutput {
			outputJSON(info)
			return
		}

		// Human-readable output
		fmt.Println("\nBeads Database Information")
		fmt.Println("===========================")
		fmt.Printf("Database: %s\n", absDBPath)
		fmt.Printf("Mode: %s\n", daemonStatus.Mode)

		if daemonClient != nil {
			fmt.Println("\nDaemon Status:")
			fmt.Printf("  Connected: yes\n")
			fmt.Printf("  Socket: %s\n", daemonStatus.SocketPath)

			health, err := daemonClient.Health()
			if err == nil {
				fmt.Printf("  Version: %s\n", health.Version)
				fmt.Printf("  Health: %s\n", health.Status)
				if health.Compatible {
					fmt.Printf("  Compatible: ✓ yes\n")
				} else {
					fmt.Printf("  Compatible: ✗ no (restart recommended)\n")
				}
				fmt.Printf("  Uptime: %.1fs\n", health.Uptime)
			}
		} else {
			fmt.Println("\nDaemon Status:")
			fmt.Printf("  Connected: no\n")
			if daemonStatus.FallbackReason != "" && daemonStatus.FallbackReason != FallbackNone {
				fmt.Printf("  Reason: %s\n", daemonStatus.FallbackReason)
			}
			if daemonStatus.Detail != "" {
				fmt.Printf("  Detail: %s\n", daemonStatus.Detail)
			}
		}

		// Show issue count
		if count, ok := info["issue_count"].(int); ok {
			fmt.Printf("\nIssue Count: %d\n", count)
		}

		// Show schema information if requested
		if schemaFlag {
			if schemaInfo, ok := info["schema"].(map[string]interface{}); ok {
				fmt.Println("\nSchema Information:")
				fmt.Printf("  Tables: %v\n", schemaInfo["tables"])
				if version, ok := schemaInfo["schema_version"].(string); ok {
					fmt.Printf("  Schema Version: %s\n", version)
				}
				if prefix, ok := schemaInfo["detected_prefix"].(string); ok && prefix != "" {
					fmt.Printf("  Detected Prefix: %s\n", prefix)
				}
				if samples, ok := schemaInfo["sample_issue_ids"].([]string); ok && len(samples) > 0 {
					fmt.Printf("  Sample Issues: %v\n", samples)
				}
			}
		}

		fmt.Println()
	},
}

// extractPrefix extracts the prefix from an issue ID (e.g., "bd-123" -> "bd")
func extractPrefix(issueID string) string {
	parts := strings.Split(issueID, "-")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func init() {
	infoCmd.Flags().Bool("schema", false, "Include schema information in output")
	infoCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.AddCommand(infoCmd)
}
