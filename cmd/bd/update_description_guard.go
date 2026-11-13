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

// clearDescriptionRegistered reports whether cmd registers --clear-description.
// The registration (not the flag's value) is what marks the update contract:
// updateCmd registers the GH#8149 clear verbs, createCmd does not, and
// validateDescriptionUpdate refuses inline empty replacements only where a
// deliberate-clear verb exists to name. A command that forgets to register
// the flag silently keeps the old create behavior, which is why
// TestClearTextFlagsRegisteredOnUpdateOnly pins the registration.
func clearDescriptionRegistered(cmd *cobra.Command) bool {
	return cmd.Flags().Lookup("clear-description") != nil
}

// validateDescriptionUpdate guards the description field of an update (and,
// with a different contract, of a create).
//
// On update (GH#8149), an exactly empty inline description is refused
// outright, mirroring the GH#6021 notes guard: the empty string is exactly
// what a dead command substitution (`--description "$(cat missing.txt)"`)
// collapses to, and at the flag layer the accident and the deliberate clear
// are the same bytes. --force does not reach this guard and
// --allow-empty-description does not unblock inline input (the opt-in covers
// stdin/file input plumbing only, where emptiness is an input property): the
// deliberate clear is --clear-description.
//
// On create, inline empty stays the sanctioned explicit clear (a field may
// be born empty), and only stdin/file input keeps the
// --allow-empty-description gate.
func validateDescriptionUpdate(cmd *cobra.Command, description string, descChanged bool) error {
	if !descChanged || description != "" {
		return nil
	}
	if !descriptionUsesExternalInput(cmd) {
		if !clearDescriptionRegistered(cmd) {
			return nil
		}
		return fmt.Errorf(`--description "" would clear the description field (an empty command substitution is the usual accident); use --clear-description to clear it deliberately`)
	}

	allowEmptyDescription, _ := cmd.Flags().GetBool("allow-empty-description")
	if allowEmptyDescription {
		return nil
	}

	return fmt.Errorf("empty description from stdin/file requires --allow-empty-description (or use an explicit inline empty value like --description \"\")")
}

// validateDesignUpdate is the GH#8149 design twin of the notes guard: on
// update, an exactly empty --design — including an empty --design-file, stdin
// spelled "-" included, since the file flag resolves to the same value — is
// refused and the deliberate clear is --clear-design. Whitespace is content.
// Create does not call this: a field may be born empty.
func validateDesignUpdate(design string, designChanged bool) error {
	if !designChanged || design != "" {
		return nil
	}
	return fmt.Errorf(`--design "" (or an empty --design-file) would clear the design field (an empty command substitution is the usual accident); use --clear-design to clear it deliberately`)
}

// validateAcceptanceUpdate is the GH#8149 acceptance twin of the notes guard:
// on update, an exactly empty --acceptance (or deprecated
// --acceptance-criteria, which resolves to the same value) is refused and the
// deliberate clear is --clear-acceptance. Whitespace is content. Create does
// not call this.
func validateAcceptanceUpdate(acceptance string, acceptanceChanged bool) error {
	if !acceptanceChanged || acceptance != "" {
		return nil
	}
	return fmt.Errorf(`--acceptance "" would clear the acceptance criteria (an empty command substitution is the usual accident); use --clear-acceptance to clear it deliberately`)
}

// registerUpdateClearTextFlags registers the GH#8149 deliberate-clear verbs
// for the three long-form text fields on update commands only, with a
// mutual-exclusion pair for each clear/content combination of the same
// field. Call it after every content flag it pairs with is registered
// (MarkFlagsMutuallyExclusive panics on an unknown name), including the
// hidden acceptance-criteria alias; validateDescriptionUpdate reads the
// --clear-description registration as the update-contract marker. Create
// never calls this (AC-5: creation keeps omitted and explicitly empty
// values working, with no clear verbs).
func registerUpdateClearTextFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("clear-description", false, `Clear the description (--description "" is refused: an empty value is usually a dead command substitution)`)
	cmd.Flags().Bool("clear-design", false, `Clear the design (--design "" and an empty --design-file are refused: an empty value is usually a dead command substitution)`)
	cmd.Flags().Bool("clear-acceptance", false, `Clear the acceptance criteria (--acceptance "" is refused: an empty value is usually a dead command substitution)`)
	// A separate call per clear/content pair, as with clear-notes. Content
	// aliases of one field stay composable with one another (their own
	// precedence lives in getDescriptionFlag), and different clear flags
	// compose with one another and with other fields' content.
	cmd.MarkFlagsMutuallyExclusive("clear-description", "description")
	cmd.MarkFlagsMutuallyExclusive("clear-description", "body")
	cmd.MarkFlagsMutuallyExclusive("clear-description", "message")
	cmd.MarkFlagsMutuallyExclusive("clear-description", "body-file")
	cmd.MarkFlagsMutuallyExclusive("clear-description", "description-file")
	cmd.MarkFlagsMutuallyExclusive("clear-description", "stdin")
	cmd.MarkFlagsMutuallyExclusive("clear-design", "design")
	cmd.MarkFlagsMutuallyExclusive("clear-design", "design-file")
	cmd.MarkFlagsMutuallyExclusive("clear-acceptance", "acceptance")
	cmd.MarkFlagsMutuallyExclusive("clear-acceptance", "acceptance-criteria")
}

// clearDescriptionRequested, clearDesignRequested and clearAcceptanceRequested
// report whether the field's clear verb asked for the deliberate erase. The
// Boolean value matters, matching clearNotesRequested: --clear-x=false is not
// a clear (it updates nothing on its own). Combining a clear with the same
// field's content input is contradictory and refused by cobra's
// mutual-exclusion registration on the update command.
func clearDescriptionRequested(cmd *cobra.Command) bool {
	clear, _ := cmd.Flags().GetBool("clear-description")
	return clear
}

func clearDesignRequested(cmd *cobra.Command) bool {
	clear, _ := cmd.Flags().GetBool("clear-design")
	return clear
}

func clearAcceptanceRequested(cmd *cobra.Command) bool {
	clear, _ := cmd.Flags().GetBool("clear-acceptance")
	return clear
}
