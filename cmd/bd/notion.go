package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
)

type notionStatusClient interface {
	Status(ctx context.Context, req notion.StatusRequest) (*notion.StatusResponse, error)
}

var (
	notionNCLIBin    string
	notionDatabaseID string
	notionViewURL    string
)

var newNotionStatusClient = func(binaryPath string) notionStatusClient {
	if strings.TrimSpace(binaryPath) == "" {
		return notion.NewClient()
	}
	return notion.NewClient(notion.WithBinaryPath(binaryPath))
}

var notionCmd = &cobra.Command{
	Use:     "notion",
	GroupID: "advanced",
	Short:   "Notion integration commands",
	Long: "Synchronize issues between beads and Notion through ncli beads commands.\n\n" +
		"This integration uses the local ncli binary rather than the Notion public API directly.\n\n" +
		"Examples:\n" +
		"  bd notion status\n" +
		"  bd notion status --ncli-bin /path/to/ncli\n" +
		"  bd notion status --database-id <database-id> --view-url <view-url>",
}

var notionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Notion sync status",
	Long: "Show the current Notion sync status, including:\n" +
		"  - ncli readiness\n" +
		"  - database and view selection\n" +
		"  - schema validation status\n" +
		"  - archive capability visibility",
	RunE: runNotionStatus,
}

func init() {
	notionStatusCmd.Flags().StringVar(&notionNCLIBin, "ncli-bin", "", "Path to the ncli binary")
	notionStatusCmd.Flags().StringVar(&notionDatabaseID, "database-id", "", "Override the Notion database ID")
	notionStatusCmd.Flags().StringVar(&notionViewURL, "view-url", "", "Override the Notion view URL")

	notionCmd.AddCommand(notionStatusCmd)
	rootCmd.AddCommand(notionCmd)
}

func runNotionStatus(cmd *cobra.Command, _ []string) error {
	client := newNotionStatusClient(notionNCLIBin)
	resp, err := client.Status(cmd.Context(), notion.StatusRequest{
		DatabaseID: notionDatabaseID,
		ViewURL:    notionViewURL,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		outputJSON(resp)
		return nil
	}

	renderNotionStatus(cmd, resp)
	return nil
}

func renderNotionStatus(cmd *cobra.Command, resp *notion.StatusResponse) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Notion Sync Status")
	fmt.Fprintln(out, "==================")
	fmt.Fprintln(out)

	if resp == nil {
		fmt.Fprintln(out, "Status: unknown")
		return
	}

	fmt.Fprintf(out, "Ready: %s\n", yesNo(resp.Ready))
	if resp.Database != nil {
		if resp.Database.Title != "" {
			fmt.Fprintf(out, "Database: %s\n", resp.Database.Title)
		}
		if resp.Database.ID != "" {
			fmt.Fprintf(out, "Database ID: %s\n", resp.Database.ID)
		}
	}
	if resp.DataSourceID != "" {
		fmt.Fprintf(out, "Data Source ID: %s\n", resp.DataSourceID)
	}
	if len(resp.Views) > 0 {
		fmt.Fprintf(out, "Views: %d\n", len(resp.Views))
	}
	if resp.Schema != nil && len(resp.Schema.Missing) > 0 {
		fmt.Fprintf(out, "Schema Missing: %s\n", strings.Join(resp.Schema.Missing, ", "))
	}
	if resp.Archive != nil {
		if resp.Archive.Supported {
			fmt.Fprintln(out, "Archive Support: available")
		} else {
			fmt.Fprintln(out, "Archive Support: unavailable")
			if resp.Archive.Reason != "" {
				fmt.Fprintf(out, "Archive Reason: %s\n", resp.Archive.Reason)
			}
		}
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
