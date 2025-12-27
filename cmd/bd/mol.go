package main

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
)

// Molecule commands - work templates for agent workflows
//
// Terminology:
//   - Proto: Uninstantiated template (easter egg: 'protomolecule' alias)
//   - Molecule: A spawned instance of a proto
//   - Spawn: Instantiate a proto, creating real issues from the template
//   - Bond: Polymorphic combine operation (proto+proto, proto+mol, mol+mol)
//   - Distill: Extract ad-hoc epic → reusable proto
//   - Compound: Result of bonding
//
// Usage:
//   bd mol show <id>                      # Show proto/molecule structure
//   bd mol pour <id> --var key=value      # Instantiate proto → persistent mol
//   bd mol wisp <id> --var key=value      # Instantiate proto → ephemeral wisp

// MoleculeLabel is the label used to identify molecules (templates)
// Molecules use the same label as templates - they ARE templates with workflow semantics
const MoleculeLabel = BeadsTemplateLabel

// MoleculeSubgraph is an alias for TemplateSubgraph
// Molecules and templates share the same subgraph structure
type MoleculeSubgraph = TemplateSubgraph

var molCmd = &cobra.Command{
	Use:     "mol",
	Aliases: []string{"protomolecule"}, // Easter egg for The Expanse fans
	Short:   "Molecule commands (work templates)",
	Long: `Manage molecules - work templates for agent workflows.

Protos are template epics with the "template" label. They define a DAG of work
that can be spawned to create real issues (molecules).

The molecule metaphor:
  - A proto is an uninstantiated template (reusable work pattern)
  - Spawning creates a molecule (real issues) from the proto
  - Variables ({{key}}) are substituted during spawning
  - Bonding combines protos or molecules into compounds
  - Distilling extracts a proto from an ad-hoc epic

Commands:
  show       Show proto/molecule structure and variables
  pour       Instantiate proto as persistent mol (liquid phase)
  wisp       Instantiate proto as ephemeral wisp (vapor phase)
  bond       Polymorphic combine: proto+proto, proto+mol, mol+mol
  squash     Condense molecule to digest
  burn       Discard wisp
  distill    Extract proto from ad-hoc epic

Use "bd formula list" to list available formulas.`,
}

// =============================================================================
// Molecule Helper Functions
// =============================================================================

// spawnMolecule creates new issues from the proto with variable substitution.
// This instantiates a proto (template) into a molecule (real issues).
// Wraps cloneSubgraph from template.go and returns InstantiateResult.
// If ephemeral is true, spawned issues are marked for bulk deletion when closed.
// The prefix parameter overrides the default issue prefix (bd-hobo: distinct prefixes).
func spawnMolecule(ctx context.Context, s storage.Storage, subgraph *MoleculeSubgraph, vars map[string]string, assignee string, actorName string, ephemeral bool, prefix string) (*InstantiateResult, error) {
	opts := CloneOptions{
		Vars:     vars,
		Assignee: assignee,
		Actor:    actorName,
		Ephemeral:     ephemeral,
		Prefix:   prefix,
	}
	return cloneSubgraph(ctx, s, subgraph, opts)
}

// spawnMoleculeWithOptions creates new issues from the proto using CloneOptions.
// This allows full control over dynamic bonding, variable substitution, and wisp phase.
func spawnMoleculeWithOptions(ctx context.Context, s storage.Storage, subgraph *MoleculeSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	return cloneSubgraph(ctx, s, subgraph, opts)
}

// printMoleculeTree prints the molecule structure as a tree
func printMoleculeTree(subgraph *MoleculeSubgraph, parentID string, depth int, isRoot bool) {
	printTemplateTree(subgraph, parentID, depth, isRoot)
}

func init() {
	rootCmd.AddCommand(molCmd)
}
