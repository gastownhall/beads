package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
)

var importCmd = &cobra.Command{
	Use:   "import [file]",
	Short: "Import issues from a JSONL file into the database",
	Long: `Import issues from a JSONL file (newline-delimited JSON) into the database.

If no file is specified, imports from .beads/issues.jsonl (the git-tracked
export). This is the incremental counterpart to 'bd export': new issues are
created and existing issues are updated (upsert semantics).

Memory records (lines with "_type":"memory") are automatically detected and
imported as persistent memories (equivalent to 'bd remember'). This makes
'bd export | bd import' a full round-trip for both issues and memories.

This command makes the git-tracked JSONL portable again — after 'git pull'
brings new issues, 'bd import' loads them into the local Dolt database.

EXAMPLES:
  bd import                        # Import from .beads/issues.jsonl
  bd import backup.jsonl           # Import from a specific file
  bd import --dry-run              # Show what would be imported`,
	GroupID: "sync",
	RunE:    runImport,
}

var (
	importDryRun bool
)

func init() {
	importCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Show what would be imported without importing")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	ctx := rootCtx

	// Determine source file
	var jsonlPath string
	if len(args) > 0 {
		jsonlPath = args[0]
	} else {
		// Default: .beads/issues.jsonl
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return fmt.Errorf("%s — %s", activeWorkspaceNotFoundError(), diagHint())
		}
		jsonlPath = filepath.Join(beadsDir, "issues.jsonl")
	}

	// Check file exists
	info, err := os.Stat(jsonlPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", jsonlPath, err)
	}
	if info.Size() == 0 {
		fmt.Fprintf(os.Stderr, "Empty file: %s\n", jsonlPath)
		return nil
	}

	if importDryRun {
		fmt.Fprintf(os.Stderr, "Would import from: %s (%d bytes)\n", jsonlPath, info.Size())
		return nil
	}

	// store is the global Dolt store, opened by main.go's PersistentPreRunE
	if store == nil {
		return fmt.Errorf("no database — run 'bd init' or 'bd bootstrap' first")
	}

	result, err := importFromLocalJSONLFull(ctx, store, jsonlPath)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	commitMsg := fmt.Sprintf("bd import: %d issues", result.Issues)
	if result.Memories > 0 {
		commitMsg += fmt.Sprintf(", %d memories", result.Memories)
	}
	commitMsg += fmt.Sprintf(" from %s", filepath.Base(jsonlPath))
	if err := store.Commit(ctx, commitMsg); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, "Imported %d issues and %d memories from %s\n", result.Issues, result.Memories, jsonlPath)
	} else {
		fmt.Fprintf(os.Stderr, "Imported %d issues from %s\n", result.Issues, jsonlPath)
	}
	return nil
}
