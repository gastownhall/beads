//go:build !cgo

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func noCGODoltError(cmd *cobra.Command, args []string) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"error":   "dolt_not_available",
			"message": "Dolt commands require CGO. This binary was built without CGO support.",
		})
	} else {
		fmt.Fprintf(os.Stderr, "Error: Dolt commands require CGO\n")
		fmt.Fprintf(os.Stderr, "This binary was built without CGO support.\n")
		fmt.Fprintf(os.Stderr, "To use Dolt, rebuild with: CGO_ENABLED=1 go build\n")
	}
	os.Exit(1)
}

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: "setup",
	Short:   "Configure Dolt database settings",
	Long:    `Dolt commands require CGO. This binary was built without CGO support.`,
	Run:     noCGODoltError,
}

var doltShowCmdNoCGO = &cobra.Command{
	Use:   "show",
	Short: "Show current Dolt configuration with connection status",
	Run:   noCGODoltError,
}

var doltSetCmdNoCGO = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a Dolt configuration value",
	Args:  cobra.ExactArgs(2),
	Run:   noCGODoltError,
}

var doltTestCmdNoCGO = &cobra.Command{
	Use:   "test",
	Short: "Test connection to Dolt server",
	Run:   noCGODoltError,
}

var doltStartCmdNoCGO = &cobra.Command{
	Use:   "start",
	Short: "Start a Dolt SQL server using configured settings",
	Run:   noCGODoltError,
}

var doltStopCmdNoCGO = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running Dolt SQL server",
	Run:   noCGODoltError,
}

func init() {
	doltSetCmdNoCGO.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltCmd.AddCommand(doltShowCmdNoCGO)
	doltCmd.AddCommand(doltSetCmdNoCGO)
	doltCmd.AddCommand(doltTestCmdNoCGO)
	doltCmd.AddCommand(doltStartCmdNoCGO)
	doltCmd.AddCommand(doltStopCmdNoCGO)
	rootCmd.AddCommand(doltCmd)
}
