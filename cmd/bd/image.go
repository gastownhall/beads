package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

var imageCmd = &cobra.Command{
	Use:     "image <issue-id> [issue-id...]",
	GroupID: "issues",
	Short:   "Print raw, hashable images of issues as JSON lines",
	Long: `Print the raw image of each issue: one line of compact JSON per id, in
the order given (duplicates collapse).

An image is every stored row that belongs to the issue, read in one
transaction for all ids: its own row, its labels, its comments, its
events, and its dependencies in both directions. Rows are raw: every
column by its column name, SQL NULL as null, JSON columns as the stored
text, timestamps as UTC RFC 3339 at the column's stored precision.
Keys are sorted and rows are sorted by primary key.

Generated columns are excluded from the image and from the revision.

The "revision" field is the sha256 (hex) of the canonical image without
the revision field. It changes on any write to the issue - its body, a
label, a comment, an event, a dependency in either direction, a status
change - so an archive can later check that an issue is unchanged.

If any id names no issue, nothing is printed and the command exits 1
naming every missing id.

Examples:
  bd image bd-1
  bd image bd-1 bd-2
  bd image --from-file ids.txt`,
	Args:          cobra.MinimumNArgs(0),
	SilenceUsage:  true,
	SilenceErrors: true,
	// No write of any kind, startup and post-command maintenance included:
	// every invocation takes the --dry-run preview policy, so the root
	// pre-run records no version, runs no post-upgrade migration or
	// reconciliation, opens the store or provider without schema
	// initialization, and PostRunE runs no auto-commit, export or backup.
	Annotations: map[string]string{mutationFreeAnnotation: "true"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ids := append([]string{}, args...)
		if fromFile, _ := cmd.Flags().GetString("from-file"); fromFile != "" {
			fileIDs, err := readIssueIDsFromFile(fromFile)
			if err != nil {
				return HandleError("reading file: %v", err)
			}
			ids = append(ids, fileIDs...)
		}
		if len(ids) == 0 {
			_ = cmd.Usage()
			return HandleError("no issue IDs provided")
		}
		ids = uniqueStrings(ids)

		if usesProxiedServer() {
			return runImageProxiedServer(rootCtx, ids)
		}
		return runImageDirect(rootCtx, ids)
	},
}

// imagerSource is the role accessor the stores offer. It is not on
// storage.DoltStorage, so it is asked of the unwrapped store; a read needs
// none of the decorators.
type imagerSource interface {
	Imager() (issueops.Imager, error)
}

// runImageDirect is `bd image` on a local or server-mode store.
//
// Ids pass the front door's resolution — prefix matching and cross-repository
// routing — as `bd delete` resolves them, and reach the role exact. The role
// then reads them all on one transaction, which is why every id must resolve
// to ONE store: an id routed to another repository can only be imaged on its
// own.
func runImageDirect(ctx context.Context, ids []string) error {
	base := store
	if base == nil {
		// Not ensureStoreActive: that opens the ordinary writable store. A
		// late open takes the same non-mutating preview open the root pre-run
		// gives this command.
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return HandleError("%v.\nHint: run 'bd init' to create a database in the current directory", ErrNoBeadsDatabase)
		}
		opened, err := newPreviewStoreFromConfig(ctx, beadsDir)
		if err != nil {
			return HandleError("failed to open database: %v", err)
		}
		defer func() { _ = opened.Close() }()
		base = opened
	}
	resolved := make([]string, 0, len(ids))
	var missing []string
	var routedStore storage.DoltStorage
	routed := 0
	for _, id := range ids {
		result, err := resolveAndGetIssueWithRouting(ctx, base, id)
		if err != nil {
			if isNotFoundErr(err) {
				missing = append(missing, id)
				continue
			}
			return HandleError("%v", err)
		}
		resolved = append(resolved, result.ResolvedID)
		if result.Routed {
			routed++
			if routedStore == nil {
				routedStore = result.Store
				continue
			}
		}
		result.Close()
	}
	if routedStore != nil {
		defer func() { _ = routedStore.Close() }()
	}
	if len(missing) > 0 {
		return HandleError("%v", &issueops.NotFoundError{IDs: missing})
	}
	imageStore := base
	if routedStore != nil {
		if routed != len(ids) || len(ids) > 1 {
			return HandleError("bd image reads one repository in one transaction: image an id from another repository on its own")
		}
		imageStore = routedStore
	}

	source, ok := storage.UnwrapStore(imageStore).(imagerSource)
	if !ok {
		return HandleError("storage backend %T does not offer the Imager accessor", storage.UnwrapStore(imageStore))
	}
	imager, err := source.Imager()
	if err != nil {
		return HandleError("%v", err)
	}
	return printImages(ctx, imager, uniqueStrings(resolved))
}

// printImages reads every image on the role's one transaction and prints one
// canonical JSON line each. Nothing is printed unless every id imaged, so a
// missing id leaves stdout empty.
func printImages(ctx context.Context, imager issueops.Imager, ids []string) error {
	images, err := imager.Image(ctx, ids)
	if err != nil {
		return HandleError("%v", err)
	}
	lines := make([][]byte, 0, len(images))
	for _, img := range images {
		line, err := storeissueops.CanonicalImageJSON(img)
		if err != nil {
			return HandleError("encode image %s: %v", img.ID, err)
		}
		lines = append(lines, line)
	}
	w := bufio.NewWriter(os.Stdout)
	for _, line := range lines {
		_, _ = w.Write(line)
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write images: %w", err)
	}
	return nil
}

func init() {
	imageCmd.Flags().String("from-file", "", "Read issue IDs from file (one per line)")
	imageCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(imageCmd)
	readOnlyCommands["image"] = true
}
