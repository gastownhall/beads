package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var shipCmd = &cobra.Command{
	Use:   "ship <capability>",
	Short: "Publish a capability for cross-project dependencies",
	Long: `Ship a capability to satisfy cross-project dependencies.

This command:
  1. Finds issue with export:<capability> label
  2. Validates issue is closed (or --force to override)
  3. Adds provides:<capability> label

External projects can depend on this capability using:
  bd dep add <issue> external:<project>:<capability>

The capability is resolved when the external project has a closed issue
with the provides:<capability> label.

Examples:
  bd ship mol-run-assignee              # Ship the mol-run-assignee capability
  bd ship mol-run-assignee --force      # Ship even if issue is not closed
  bd ship mol-run-assignee --dry-run    # Preview without making changes`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runShip,
}

type capabilityShipStore interface {
	GetIssuesByLabel(context.Context, string) ([]*types.Issue, error)
	GetLabels(context.Context, string) ([]string, error)
	AddLabel(context.Context, string, string, string) error
}

// proxiedCapabilityShipStore reads through the caller's unit of work and adds
// the provides: label with the guarded issue updater, inside the same
// transaction.
type proxiedCapabilityShipStore struct{ uowMolReader }

func (s proxiedCapabilityShipStore) AddLabel(ctx context.Context, id, label, actor string) error {
	_, err := s.uw.IssueUseCase().ApplyUpdate(ctx, id, domain.UpdateSpec{AddLabels: []string{label}}, actor)
	return err
}

type capabilityShipment struct {
	status  string
	issue   *types.Issue
	matches []*types.Issue
}

func runShip(cmd *cobra.Command, args []string) error {
	CheckReadonly("ship")

	evt := metrics.NewCommandEvent("ship")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	capability := args[0]
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	var result capabilityShipment
	var err error
	if usesProxiedServer() {
		result, err = uow.RunTxResult(rootCtx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (capabilityShipment, string, error) {
			result, err := shipCapability(ctx, proxiedCapabilityShipStore{uowMolReader{uw: uw}}, capability, force, dryRun)
			message := ""
			if err == nil && result.status == "shipped" {
				message = "bd: ship " + capability + " on " + result.issue.ID
			}
			return result, message, err
		})
	} else {
		result, err = shipCapability(rootCtx, store, capability, force, dryRun)
	}
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if result.status == "shipped" {
		commandDidWrite.Store(true)
	}
	return reportCapabilityShipment(capability, result)
}

// shipCapability applies the same selection and publication rules on both
// routes. A proxied caller owns the surrounding transaction, so nothing is
// printed here: all output waits until that attempt has committed.
func shipCapability(ctx context.Context, s capabilityShipStore, capability string, force, dryRun bool) (capabilityShipment, error) {
	issues, err := s.GetIssuesByLabel(ctx, "export:"+capability)
	if err != nil {
		return capabilityShipment{}, fmt.Errorf("listing issues: %w", err)
	}
	if len(issues) == 0 {
		return capabilityShipment{status: "not_found"}, nil
	}
	if len(issues) > 1 {
		return capabilityShipment{status: "ambiguous", matches: issues}, nil
	}
	result := capabilityShipment{issue: issues[0]}
	if result.issue.Status != types.StatusClosed && !force {
		result.status = "not_closed"
		return result, nil
	}
	labels, err := s.GetLabels(ctx, result.issue.ID)
	if err != nil {
		return capabilityShipment{}, fmt.Errorf("getting labels: %w", err)
	}
	for _, label := range labels {
		if label == "provides:"+capability {
			result.status = "already_shipped"
			return result, nil
		}
	}
	if dryRun {
		result.status = "dry_run"
		return result, nil
	}
	if err := s.AddLabel(ctx, result.issue.ID, "provides:"+capability, currentActor()); err != nil {
		return capabilityShipment{}, fmt.Errorf("adding label: %w", err)
	}
	result.status = "shipped"
	return result, nil
}

func reportCapabilityShipment(capability string, result capabilityShipment) error {
	exportLabel := "export:" + capability
	providesLabel := "provides:" + capability
	switch result.status {
	case "not_found":
		return HandleErrorWithHintRespectJSON(
			fmt.Sprintf("no issue found with label '%s'", exportLabel),
			labelAddCommand(exportLabel))
	case "ambiguous":
		fmt.Fprintf(os.Stderr, "Error: multiple issues found with label '%s':\n", exportLabel)
		for _, issue := range result.matches {
			fmt.Fprintf(os.Stderr, "  %s: %s (%s)\n", issue.ID, issue.Title, issue.Status)
		}
		return HandleErrorRespectJSON("only one issue should have this label")
	case "not_closed":
		return HandleErrorWithHintRespectJSON(
			fmt.Sprintf("issue %s is not closed (status: %s)", result.issue.ID, result.issue.Status),
			"close the issue first, or use --force to override")
	}

	if jsonOutput {
		value := map[string]interface{}{
			"status":     result.status,
			"capability": capability,
			"issue_id":   result.issue.ID,
		}
		switch result.status {
		case "dry_run":
			value["would_add"] = providesLabel
		case "shipped":
			value["label"] = providesLabel
		}
		return outputJSON(value)
	}
	switch result.status {
	case "already_shipped":
		fmt.Printf("%s Capability '%s' already shipped (%s)\n",
			ui.RenderPass("✓"), capability, result.issue.ID)
	case "dry_run":
		fmt.Printf("%s Would ship '%s' on %s (dry run)\n",
			ui.RenderAccent("→"), capability, result.issue.ID)
	case "shipped":
		fmt.Printf("%s Shipped %s (%s)\n",
			ui.RenderPass("✓"), capability, result.issue.ID)
		fmt.Printf("  Added label: %s\n", providesLabel)
		fmt.Printf("\nExternal projects can now depend on: external:%s:%s\n",
			"<this-project>", capability)
	}
	return nil
}

func init() {
	shipCmd.Flags().Bool("force", false, "Ship even if issue is not closed")
	shipCmd.Flags().Bool("dry-run", false, "Preview without making changes")

	rootCmd.AddCommand(shipCmd)
}

// labelAddCommand is the hint naming the bd label add command for label, the
// label as one shell word or a placeholder named after the command. The label
// holds the capability the user typed, as shipCommand's does.
func labelAddCommand(label string) string {
	arg, note := remedy.CommandArg(runtime.GOOS, label, "<label>")
	return "add the label first: bd label add <issue-id> " + arg + note
}
