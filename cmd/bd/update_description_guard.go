package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func descriptionUsesExternalInput(cmd *cobra.Command) bool {
	if stdinFlag, _ := cmd.Flags().GetBool("stdin"); stdinFlag {
		return true
	}
	if cmd.Flags().Changed("body-file") || cmd.Flags().Changed("description-file") {
		return true
	}
	if cmd.Flags().Changed("description") {
		desc, _ := cmd.Flags().GetString("description")
		if desc == "-" {
			return true
		}
	}
	if cmd.Flags().Changed("body") {
		body, _ := cmd.Flags().GetString("body")
		if body == "-" {
			return true
		}
	}
	if cmd.Flags().Changed("message") {
		message, _ := cmd.Flags().GetString("message")
		if message == "-" {
			return true
		}
	}
	return false
}

func validateDescriptionUpdate(cmd *cobra.Command, description string, descChanged bool) error {
	if !descChanged || description != "" || !descriptionUsesExternalInput(cmd) {
		return nil
	}

	allowEmptyDescription, _ := cmd.Flags().GetBool("allow-empty-description")
	if allowEmptyDescription {
		return nil
	}

	return fmt.Errorf("empty description from stdin/file requires --allow-empty-description (or use --description \"\" for an explicit inline clear)")
}
