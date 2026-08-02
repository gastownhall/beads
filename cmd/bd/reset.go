package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Remove all beads data and configuration",
	Long: `Reset beads to an uninitialized state, removing all local data.

This command removes:
  - The .beads directory (database, JSONL, config)
  - Git hooks installed by bd
  - Sync branch worktrees

By default, shows what would be deleted (dry-run mode).
Use --force to actually perform the reset.

Examples:
  bd reset              # Show what would be deleted
  bd reset --force      # Actually delete everything`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runReset,
}

func init() {
	resetCmd.Flags().Bool("force", false, "Actually perform the reset (required)")
	// Note: resetCmd is added to adminCmd in admin.go
}

func runReset(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("admin reset is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("admin-reset")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if err := requireServerMode("reset"); err != nil {
		return HandleError("%v", err)
	}
	CheckReadonly("reset")

	force, _ := cmd.Flags().GetBool("force")

	gitCommonDir, err := git.GetGitCommonDir()
	if err != nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error": "not a git repository",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("not a git repository")
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"message": "beads not initialized",
				"reset":   false,
			})
		}
		fmt.Println("Beads is not initialized in this repository.")
		fmt.Println("Nothing to reset.")
		return nil
	}

	home, _ := os.UserHomeDir()
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		repoRoot = filepath.Dir(gitCommonDir)
	}
	if err := refuseGlobalBeadsDir(beadsDir, repoRoot, home); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	items, preserved := collectResetItems(gitCommonDir, beadsDir)

	if !force {
		return showResetPreview(items, preserved)
	}

	return performReset(items, preserved, gitCommonDir, beadsDir)
}

type resetItem struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

// collectResetItems returns what reset will remove, and separately what it
// found but will leave alone. The second list is not cosmetic: a hook bd only
// injected a section into is the user's file, and saying nothing about it is
// how a reset looks like it did less than it did — or, before ownership was
// checked properly, more.
func collectResetItems(gitCommonDir, beadsDir string) (items, preserved []resetItem) {
	// Check for git hooks (hooks are in common git dir, shared across worktrees)
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}
	hooksDir := filepath.Join(gitCommonDir, "hooks")
	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); err != nil {
			continue
		}
		switch classifyResetHook(hookPath) {
		case hookBdOwned:
			items = append(items, resetItem{
				Type:        "hook",
				Path:        hookPath,
				Description: fmt.Sprintf("Remove git hook: %s", hookName),
			})
		case hookUserOwnedWithBdSection:
			preserved = append(preserved, resetItem{
				Type:        "hook",
				Path:        hookPath,
				Description: fmt.Sprintf("Keep git hook %s: it is your file with a beads section in it (remove the section with 'bd hooks uninstall')", hookName),
			})
		case hookNotOurs:
			// No bd marker at all. Not ours to mention, let alone remove.
		}
	}

	// Check for sync branch worktrees (in common git dir, shared across worktrees)
	worktreesDir := filepath.Join(gitCommonDir, "beads-worktrees")
	if info, err := os.Stat(worktreesDir); err == nil && info.IsDir() {
		items = append(items, resetItem{
			Type:        "worktrees",
			Path:        worktreesDir,
			Description: "Remove sync branch worktrees",
		})
	}

	// The .beads directory itself
	items = append(items, resetItem{
		Type:        "directory",
		Path:        beadsDir,
		Description: "Remove .beads directory (database, JSONL, config)",
	})

	return items, preserved
}

// refuseGlobalBeadsDir stops a repo-scoped reset from deleting the user-global
// ~/.beads directory.
//
// bd admin reset removes whatever FindBeadsDir returns, and FindBeadsDir walks
// up from the working directory with no upper boundary — repoRoot is used to
// classify a hit, never to stop the climb. So in any git repository under the
// home directory that has no .beads of its own, the walk continues past the repo
// and lands on ~/.beads. hasBeadsProjectFiles is documented as the thing that
// prevents this, but it is a content guard, not a boundary guard: it holds only
// while the global directory is not itself a real project. Once a user has one,
// it stops holding, and that is when it matters.
//
// That is the shape of the 2026-07-21 incident, where a reset aimed at a temp
// repository removed the global .beads along with hooks from an unrelated
// checkout.
//
// The narrow rule here decides nothing about what -C should mean or how the two
// target resolutions in runReset ought to be reconciled — both are open
// questions. It only says that a reset run inside some project is never a
// request to delete the user's global directory. When the repository root IS the
// home directory, ~/.beads is that repository's own beads dir and the reset is
// exactly what was asked for.
func refuseGlobalBeadsDir(beadsDir, repoRoot, home string) error {
	if home == "" {
		// Without a home directory there is no global location to protect.
		return nil
	}
	if !utils.PathsEqual(beadsDir, filepath.Join(home, ".beads")) {
		return nil
	}
	if utils.PathsEqual(repoRoot, home) {
		// The home directory is the repository. ~/.beads is its beads dir.
		return nil
	}

	return fmt.Errorf("refusing to reset: this repository has no .beads of its own, so beads resolved to the "+
		"user-global directory\n  repository:  %s\n  would remove: %s\n"+
		"Removing global beads state is not part of resetting a repository. "+
		"Delete that directory yourself if you mean to.", repoRoot, beadsDir)
}

// hookOwnership says how much of a git hook file bd is responsible for, which
// is the only question that licenses `bd admin reset` to delete it.
type hookOwnership int

const (
	// hookNotOurs: no bd provenance marker. bd did not write this file and
	// must not remove it.
	hookNotOurs hookOwnership = iota
	// hookBdOwned: bd wrote the whole file. Removing it restores exactly the
	// state before bd touched the repo, so reset removes it.
	hookBdOwned
	// hookUserOwnedWithBdSection: the user's own hook, with bd's block injected
	// between section markers (the v0.49+ model). The file is theirs; deleting
	// it would take their content with it, and reset has no backup to restore
	// because it never displaced anything here.
	hookUserOwnedWithBdSection
)

// classifyResetHook decides ownership from the file's content, using the same
// markers bd writes when it installs and the same rules preservePreexistingHooks
// applies (GH#3536), rather than a heuristic of its own.
//
// It used to ask whether any of the first ten lines contained the substring
// "beads", anywhere, in any context. That matched a comment, a path, or a call
// to any other tool that happens to spell the word — including this project's
// own hand-composed, git-tracked .githooks/pre-commit, which chains
// `bd hooks run` alongside guards bd knows nothing about. Such a hook was
// deleted as if bd had installed it, and performReset's restore path does not
// help: it renames <hook>.backup back into place, and a backup exists only for
// hooks bd itself displaced. A hand-written hook went with no way back.
func classifyResetHook(hookPath string) hookOwnership {
	// #nosec G304 -- hook path is constructed from git dir, not user input
	data, err := os.ReadFile(hookPath)
	if err != nil {
		return hookNotOurs
	}
	content := string(data)

	// Order matters, and matches shouldPreserveHookContent: a sectioned file is
	// user-owned unless stripping bd's block leaves nothing but a shebang, in
	// which case bd effectively wrote all of it. What counts as "nothing" is
	// where the two part company — see isOnlyShebangOrBlank.
	if strings.Contains(content, hookSectionBeginPrefix) {
		if stripped, _ := removeHookSection(content); isOnlyShebangOrBlank(stripped) {
			return hookBdOwned
		}
		return hookUserOwnedWithBdSection
	}
	if strings.Contains(content, inlineHookMarker) ||
		strings.Contains(content, hookVersionPrefix) ||
		strings.Contains(content, shimVersionPrefix) {
		return hookBdOwned
	}
	return hookNotOurs
}

// isOnlyShebangOrBlank reports whether what is left of a hook after bd's
// section is stripped is nothing but an optional shebang and blank lines.
// Comments count as content.
//
// This is deliberately stricter than isOnlyShebangOrEmpty (hooks.go), which
// answers the same shape of question for shouldPreserveHookContent and treats
// comments as nothing (GH#3536). The two callers are not symmetric and must not
// share a predicate:
//
//   - Preservation copies a hook forward into .beads/hooks. Declining to copy a
//     comment-only file loses a comment; the original is still on disk.
//   - Reset deletes. Getting it wrong destroys the user's file, and
//     performReset cannot undo it — its restore path renames <hook>.backup back
//     into place, and a backup exists only for hooks bd itself displaced, which
//     is never the case for a hook bd only injected a section into.
//
// So a hook of the user's whose own content is a shebang and comments — a
// header they wrote, a note to whoever edits it next, hook logic they commented
// out for now — is theirs. Reset leaves it and says so. Sharing the looser
// predicate meant a comment was once again enough to make a hook bd's to
// delete, which is the failure classifyResetHook exists to close.
func isOnlyShebangOrBlank(content string) bool {
	seenContent := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Only the leading line can be a shebang; `#!` below it is a comment,
		// and comments are content here. Anchoring on the first non-blank line
		// rather than index 0 keeps this from depending on whether stripping
		// bd's section left a blank line above the shebang.
		if !seenContent && strings.HasPrefix(trimmed, "#!") {
			seenContent = true
			continue
		}
		return false
	}
	return true
}

func showResetPreview(items, preserved []resetItem) error {
	if jsonOutput {
		result := map[string]interface{}{
			"dry_run": true,
			"items":   items,
		}
		if len(preserved) > 0 {
			result["preserved"] = preserved
		}
		return outputJSON(result)
	}

	fmt.Println(ui.RenderWarn("Reset preview (dry-run mode)"))
	fmt.Println()
	fmt.Println("The following will be removed:")
	fmt.Println()

	for _, item := range items {
		fmt.Printf("  %s %s\n", ui.RenderFail("•"), item.Description)
		if item.Type != "config" {
			fmt.Printf("    %s\n", item.Path)
		}
	}

	printPreservedHooks(preserved)

	fmt.Println()
	fmt.Println(ui.RenderFail("⚠ This operation cannot be undone!"))
	fmt.Println()
	fmt.Printf("To proceed, run: %s\n", ui.RenderWarn("bd reset --force"))
	return nil
}

// printPreservedHooks reports the hooks reset deliberately did not touch.
// Silence here is the failure mode worth avoiding: the user asked for a reset
// and needs to know a beads section is still live in a hook of theirs.
func printPreservedHooks(preserved []resetItem) {
	if len(preserved) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Left in place (not installed by bd):")
	fmt.Println()
	for _, item := range preserved {
		fmt.Printf("  %s %s\n", ui.RenderPass("•"), item.Description)
		fmt.Printf("    %s\n", item.Path)
	}
}

func performReset(items, preserved []resetItem, _, _ string) error {

	var errors []string

	for _, item := range items {
		switch item.Type {
		case "hook":
			if err := os.Remove(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove hook %s: %v", item.Path, err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed %s\n", ui.RenderPass("✓"), filepath.Base(item.Path))
			}
			// Restore backup if exists
			backupPath := item.Path + ".backup"
			if _, err := os.Stat(backupPath); err == nil {
				if err := os.Rename(backupPath, item.Path); err == nil && !jsonOutput {
					fmt.Printf("  Restored backup hook\n")
				}
			}

		case "worktrees":
			if err := os.RemoveAll(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove worktrees: %v", err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed sync worktrees\n", ui.RenderPass("✓"))
			}

		case "directory":
			if err := os.RemoveAll(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove .beads: %v", err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed .beads directory\n", ui.RenderPass("✓"))
			}
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"reset":   true,
			"success": len(errors) == 0,
		}
		if len(errors) > 0 {
			result["errors"] = errors
		}
		if len(preserved) > 0 {
			result["preserved"] = preserved
		}
		return outputJSON(result)
	}

	printPreservedHooks(preserved)

	fmt.Println()
	if len(errors) > 0 {
		fmt.Println("Completed with errors:")
		for _, e := range errors {
			fmt.Printf("  • %s\n", e)
		}
	} else {
		fmt.Printf("%s Reset complete\n", ui.RenderPass("✓"))
		fmt.Println()
		fmt.Println("To reinitialize beads, run: bd init")
	}
	return nil
}
