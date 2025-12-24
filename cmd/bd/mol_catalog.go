package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var molCatalogCmd = &cobra.Command{
	Use:     "catalog",
	Aliases: []string{"list", "ls"},
	Short:   "List available molecules",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		var molecules []*types.Issue

		if daemonClient != nil {
			resp, err := daemonClient.List(&rpc.ListArgs{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecules: %v\n", err)
				os.Exit(1)
			}
			var allIssues []*types.Issue
			if err := json.Unmarshal(resp.Data, &allIssues); err == nil {
				for _, issue := range allIssues {
					for _, label := range issue.Labels {
						if label == MoleculeLabel {
							molecules = append(molecules, issue)
							break
						}
					}
				}
			}
		} else if store != nil {
			var err error
			molecules, err = store.GetIssuesByLabel(ctx, MoleculeLabel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecules: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(molecules)
			return
		}

		if len(molecules) == 0 {
			fmt.Println("No protos available.")
			fmt.Println("\nTo create a proto:")
			fmt.Println("  1. Create an epic with child issues")
			fmt.Println("  2. Add the 'template' label: bd label add <epic-id> template")
			fmt.Println("  3. Use {{variable}} placeholders in titles/descriptions")
			fmt.Println("\nTo instantiate a molecule from a proto:")
			fmt.Println("  bd pour <id> --var key=value          # persistent mol")
			fmt.Println("  bd wisp create <id> --var key=value   # ephemeral wisp")
			return
		}

		fmt.Printf("%s\n", ui.RenderPass("Protos (for bd pour / bd wisp create):"))
		for _, mol := range molecules {
			vars := extractVariables(mol.Title + " " + mol.Description)
			varStr := ""
			if len(vars) > 0 {
				varStr = fmt.Sprintf(" (vars: %s)", strings.Join(vars, ", "))
			}
			fmt.Printf("  %s: %s%s\n", ui.RenderAccent(mol.ID), mol.Title, varStr)
		}
		fmt.Println()
	},
}

func init() {
	molCmd.AddCommand(molCatalogCmd)
}
