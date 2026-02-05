//go:build !cgo

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: "setup",
	Short:   "Configure Dolt database settings",
	Long:    `Dolt commands require CGO. This binary was built without CGO support.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(os.Stderr, "Error: Dolt commands require CGO\n")
		fmt.Fprintf(os.Stderr, "This binary was built without CGO support.\n")
		fmt.Fprintf(os.Stderr, "To use Dolt, rebuild with: CGO_ENABLED=1 go build\n")
		os.Exit(1)
	},
}

func init() {
	rootCmd.AddCommand(doltCmd)
}
