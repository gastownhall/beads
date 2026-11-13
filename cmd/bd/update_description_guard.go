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

// commandOffersClearDescription reports whether cmd follows the update
// contract (GH#8149): registering --clear-description is what makes an inline
// empty description a refused replacement rather than a value. It checks the
// registration, not the flag's value. create shares validateDescriptionUpdate
// and registers no clear flag, so it keeps accepting an inline empty value.
func commandOffersClearDescription(cmd *cobra.Command) bool {
	return cmd.Flags().Lookup("clear-description") != nil
}

// validateDescriptionUpdate guards the resolved description of create and
// update. Empty stdin/file input needs --allow-empty-description on both. On
// update (a command offering --clear-description) an inline empty value is
// refused outright, like --notes "" (GH#6021): the empty string is exactly
// what a failed command substitution collapses to, so it cannot prove intent
// (GH#8149). --force and --allow-empty-description do not open that refusal.
func validateDescriptionUpdate(cmd *cobra.Command, description string, descChanged bool) error {
	if !descChanged || description != "" {
		return nil
	}
	updateContract := commandOffersClearDescription(cmd)

	if !descriptionUsesExternalInput(cmd) {
		if !updateContract {
			return nil
		}
		return fmt.Errorf(`an empty --description/--body/--message value would clear the description (an empty command substitution is the usual accident); use --clear-description to clear it deliberately`)
	}

	allowEmptyDescription, _ := cmd.Flags().GetBool("allow-empty-description")
	if allowEmptyDescription {
		return nil
	}

	return fmt.Errorf("empty description from stdin/file requires --allow-empty-description (or use an explicit inline empty value like --description \"\")")
}

// validateDesignUpdate refuses an exactly empty design replacement on update,
// inline or from --design-file (GH#8149); --clear-design clears deliberately.
// Whitespace is content. Called only when a design flag was passed.
func validateDesignUpdate(design string) error {
	if design != "" {
		return nil
	}
	return fmt.Errorf(`an empty --design/--design-file value would clear the design field (an empty command substitution is the usual accident); use --clear-design to clear it deliberately`)
}

// validateAcceptanceUpdate refuses an exactly empty acceptance-criteria
// replacement on update (GH#8149); --clear-acceptance clears deliberately.
// Called only when --acceptance or --acceptance-criteria was passed.
func validateAcceptanceUpdate(acceptance string) error {
	if acceptance != "" {
		return nil
	}
	return fmt.Errorf(`an empty --acceptance value would clear the acceptance criteria (an empty command substitution is the usual accident); use --clear-acceptance to clear them deliberately`)
}

// updateTextClearFlags maps each GH#8149 deliberate clear flag to the update
// field it empties. Each is mutually exclusive with its field's content flags.
var updateTextClearFlags = []struct{ flag, field string }{
	{"clear-description", "description"},
	{"clear-design", "design"},
	{"clear-acceptance", "acceptance_criteria"},
}

// applyTextFieldClears adds an empty value for every clear flag set to true,
// like --clear-notes: --clear-description=false alone erases nothing.
func applyTextFieldClears(cmd *cobra.Command, fields map[string]any) {
	for _, c := range updateTextClearFlags {
		if clear, _ := cmd.Flags().GetBool(c.flag); clear {
			fields[c.field] = ""
		}
	}
}
