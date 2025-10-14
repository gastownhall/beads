package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

const (
	// Version is the current version of bd
	Version = "0.9.2"
	// Build can be set via ldflags at compile time
	Build = "dev"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			outputJSON(map[string]string{
				"version": Version,
				"build":   Build,
			})
		} else {
			fmt.Printf("bd version %s (%s)\n", Version, Build)
		}
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
