//go:build !cgo || windows
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var federationCmd = &cobra.Command{
	Use:     "federation",
	GroupID: "sync",
	Short:   "Manage peer-to-peer federation (requires CGO)",
	Long: `Federation commands require CGO and the Dolt storage backend.

This binary was built without CGO support. To use federation features:
  1. Use pre-built binaries from GitHub releases, or
  2. Build from source with CGO enabled

Federation enables synchronized issue tracking across multiple Gas Towns,
each maintaining their own Dolt database while sharing updates via remotes.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Federation requires CGO and Dolt backend.")
		fmt.Println("")
		fmt.Println("This binary was built without CGO support. To use federation:")
		fmt.Println("  1. Download pre-built binaries from GitHub releases")
		fmt.Println("  2. Or build from source with CGO enabled")
	},
}

func init() {
	rootCmd.AddCommand(federationCmd)
}

