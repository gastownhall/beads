package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
)

// mailCmd delegates to an external mail provider.
// This enables agents to use 'bd mail' consistently, while the actual
// mail implementation is provided by the orchestrator.
var mailCmd = &cobra.Command{
	Use:   "mail [subcommand] [args...]",
	Short: "Delegate to mail provider (e.g., gt mail)",
	Long: `Delegates mail operations to an external mail provider.

Agents often type 'bd mail' when working with beads, but mail functionality
is typically provided by the orchestrator. This command bridges that gap
by delegating to the configured mail provider.

Configuration (checked in order):
  1. BEADS_MAIL_DELEGATE or BD_MAIL_DELEGATE environment variable
  2. 'mail.delegate' in user-global config (~/.config/bd/config.yaml;
     project .beads/config.yaml and workspace database settings are ignored)

Examples:
  # Configure delegation (one-time setup in ~/.config/bd/config.yaml)
  export BEADS_MAIL_DELEGATE="gt mail"
  # or
  bd config set mail.delegate "gt mail"

  # Then use bd mail as if it were gt mail
  bd mail inbox                    # Lists inbox
  bd mail send mayor/ -s "Hi"      # Sends mail
  bd mail read msg-123             # Reads a message`,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	SilenceErrors:      true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("mail")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		for _, arg := range args {
			if arg == "--help" || arg == "-h" {
				_ = cmd.Help()
				return nil
			}
		}

		delegate := findMailDelegate()
		if delegate == "" {
			return HandleErrorWithHint(
				"no mail delegate configured",
				"Set BEADS_MAIL_DELEGATE=\"gt mail\" or run: bd config set mail.delegate \"gt mail\"")
		}

		parts := strings.Fields(delegate)
		if len(parts) == 0 {
			return HandleError("invalid mail delegate: %q", delegate)
		}

		cmdName := parts[0]
		cmdArgs := append(parts[1:], args...)

		// #nosec G204 - cmdName comes from user configuration (mail_delegate setting)
		execCmd := exec.Command(cmdName, cmdArgs...)
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr

		if err := execCmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return &exitError{Code: exitErr.ExitCode()}
			}
			return HandleError("running %s: %v", delegate, err)
		}
		return nil
	},
}

// findMailDelegate checks for mail delegation configuration
// Priority: env vars > user-global bd config
func findMailDelegate() string {
	// Check environment variables first
	if delegate := os.Getenv("BEADS_MAIL_DELEGATE"); delegate != "" {
		return delegate
	}
	if delegate := os.Getenv("BD_MAIL_DELEGATE"); delegate != "" {
		return delegate
	}

	if delegate := config.GetUserYamlConfig("mail.delegate"); delegate != "" {
		return delegate
	}
	if store != nil && rootCtx != nil {
		if dbVal, err := store.GetConfig(rootCtx, "mail.delegate"); err == nil && strings.TrimSpace(dbVal) != "" {
			fmt.Fprintln(os.Stderr, "beads: ignoring mail.delegate from workspace database (re-run 'bd config set mail.delegate <cmd>' to save it to your user config)")
		}
	}
	return ""
}

func init() {
	rootCmd.AddCommand(mailCmd)
}
