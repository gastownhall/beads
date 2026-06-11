package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	attachfs "github.com/steveyegge/beads/internal/attachments"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type attachmentListItem struct {
	ID               string    `json:"id"`
	IssueID          string    `json:"issue_id"`
	HashAlgorithm    string    `json:"hash_algorithm"`
	ContentHash      string    `json:"content_hash"`
	ShortHash        string    `json:"short_hash"`
	OriginalFilename string    `json:"original_filename"`
	MimeType         string    `json:"mime_type"`
	ByteSize         int64     `json:"byte_size"`
	Size             string    `json:"size"`
	StorageRelPath   string    `json:"storage_relpath"`
	Missing          bool      `json:"missing"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

type unreachableAttachmentFile struct {
	StorageRelPath string `json:"storage_relpath"`
	Path           string `json:"path"`
	ByteSize       int64  `json:"byte_size"`
	Size           string `json:"size"`
}

type attachmentMaintenanceResult struct {
	Status      string                      `json:"status"`
	Count       int                         `json:"count"`
	Unreachable []unreachableAttachmentFile `json:"unreachable"`
	DryRun      bool                        `json:"dry_run,omitempty"`
}

var attachmentCmd = &cobra.Command{
	Use:     "attachment",
	Aliases: []string{"attachments"},
	GroupID: "issues",
	Short:   "Manage issue file attachments",
}

var attachmentAddCmd = &cobra.Command{
	Use:   "add [issue-id] [path]",
	Short: "Attach a file to an issue",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("attachment add")
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("adding attachment: %v", err)
		}

		ctx := rootCtx
		result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", args[0])
		}
		defer result.Close()
		if err := validateIssueUpdatable(args[0], result.Issue); err != nil {
			FatalErrorRespectJSON("%s", err)
		}

		stored, err := attachfs.Store(result.Store, result.ResolvedID, args[1])
		if err != nil {
			FatalErrorRespectJSON("storing attachment: %v", err)
		}

		attachment, err := result.Store.AddAttachment(ctx, &types.Attachment{
			IssueID:          result.ResolvedID,
			HashAlgorithm:    stored.HashAlgorithm,
			ContentHash:      stored.ContentHash,
			OriginalFilename: stored.OriginalFilename,
			MimeType:         stored.MimeType,
			ByteSize:         stored.ByteSize,
			StorageRelPath:   stored.StorageRelPath,
			CreatedBy:        getActorWithGit(),
		})
		if err != nil {
			if !attachmentRelPathReferenced(ctx, result.Store, result.ResolvedID, stored.StorageRelPath, "") {
				_ = attachfs.RemoveStoredFile(result.Store, &types.Attachment{StorageRelPath: stored.StorageRelPath})
			}
			FatalErrorRespectJSON("adding attachment metadata: %v", err)
		}
		if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
			Command:  "attachment add",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			FatalErrorRespectJSON("failed to commit: %v", err)
		}

		commandDidWrite.Store(true)
		SetLastTouchedID(result.ResolvedID)
		if jsonOutput {
			outputJSON(attachmentListEntry(result.Store, attachment))
			return
		}
		fmt.Printf("%s Attached %s to %s (%s, %s)\n",
			ui.RenderPass("✓"),
			attachment.OriginalFilename,
			formatFeedbackID(result.ResolvedID, result.Issue.Title),
			shortAttachmentHash(attachment.ContentHash),
			formatBytes(attachment.ByteSize))
	},
}

var attachmentListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List attachments for an issue",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("listing attachments: %v", err)
		}

		ctx := rootCtx
		result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", args[0])
		}
		defer result.Close()

		attachments, err := result.Store.ListAttachments(ctx, result.ResolvedID)
		if err != nil {
			FatalErrorRespectJSON("listing attachments: %v", err)
		}
		items := attachmentListEntries(result.Store, attachments)
		if jsonOutput {
			outputJSON(items)
			return
		}
		if len(items) == 0 {
			fmt.Printf("No attachments on %s\n", result.ResolvedID)
			return
		}

		fmt.Printf("\nAttachments on %s:\n\n", result.ResolvedID)
		for _, item := range items {
			missing := ""
			if item.Missing {
				missing = " missing"
			}
			fmt.Printf("  %s  %s  %s  %s%s\n",
				item.ShortHash,
				item.OriginalFilename,
				item.MimeType,
				item.Size,
				missing)
		}
	},
}

var attachmentCopyCmd = &cobra.Command{
	Use:   "copy [issue-id] [filename-or-hash] [target]",
	Short: "Copy an attachment out to a file or directory",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("copying attachment: %v", err)
		}
		force, _ := cmd.Flags().GetBool("force")

		ctx := rootCtx
		result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", args[0])
		}
		defer result.Close()

		attachment, err := result.Store.ResolveAttachment(ctx, result.ResolvedID, args[1])
		if err != nil {
			FatalErrorRespectJSON("resolving attachment: %v", err)
		}
		targetPath, err := attachfs.CopyOut(result.Store, attachment, args[2], force)
		if err != nil {
			FatalErrorRespectJSON("copying attachment: %v", err)
		}
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "copied",
				"issue_id":      result.ResolvedID,
				"attachment_id": attachment.ID,
				"target":        targetPath,
			})
			return
		}
		fmt.Printf("%s Copied %s to %s\n", ui.RenderPass("✓"), attachment.OriginalFilename, targetPath)
	},
}

var attachmentRemoveCmd = &cobra.Command{
	Use:     "remove [issue-id] [filename-or-hash]",
	Aliases: []string{"rm"},
	Short:   "Remove an attachment from an issue",
	Args:    cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("attachment remove")
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("removing attachment: %v", err)
		}

		ctx := rootCtx
		result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", args[0])
		}
		defer result.Close()
		if err := validateIssueUpdatable(args[0], result.Issue); err != nil {
			FatalErrorRespectJSON("%s", err)
		}

		attachment, err := result.Store.ResolveAttachment(ctx, result.ResolvedID, args[1])
		if err != nil {
			FatalErrorRespectJSON("resolving attachment: %v", err)
		}
		referencedByOther := attachmentRelPathReferenced(ctx, result.Store, result.ResolvedID, attachment.StorageRelPath, attachment.ID)
		if err := result.Store.RemoveAttachment(ctx, result.ResolvedID, attachment.ID); err != nil {
			FatalErrorRespectJSON("removing attachment metadata: %v", err)
		}
		commandDidWrite.Store(true)
		if !referencedByOther {
			if err := attachfs.RemoveStoredFile(result.Store, attachment); err != nil {
				FatalErrorRespectJSON("removing attachment file: %v", err)
			}
		}
		if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
			Command:  "attachment remove",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			FatalErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "removed",
				"issue_id":      result.ResolvedID,
				"attachment_id": attachment.ID,
				"file_removed":  !referencedByOther,
			})
			return
		}
		fmt.Printf("%s Removed %s from %s\n",
			ui.RenderPass("✓"),
			attachment.OriginalFilename,
			formatFeedbackID(result.ResolvedID, result.Issue.Title))
	},
}

var attachmentFsckCmd = &cobra.Command{
	Use:   "fsck",
	Short: "Find unreachable attachment files",
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("checking attachments: %v", err)
		}

		unreachable, err := findUnreachableAttachmentFiles(rootCtx, store)
		if err != nil {
			FatalErrorRespectJSON("checking attachments: %v", err)
		}
		if jsonOutput {
			outputJSON(attachmentMaintenanceResult{
				Status:      "ok",
				Count:       len(unreachable),
				Unreachable: unreachable,
			})
			return
		}
		if len(unreachable) == 0 {
			fmt.Println("No unreachable attachment files found")
			return
		}
		fmt.Printf("Unreachable attachment files (%d):\n\n", len(unreachable))
		for _, file := range unreachable {
			fmt.Printf("  %s  %s\n", file.Size, file.StorageRelPath)
		}
	},
}

var attachmentPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove unreachable attachment files",
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("attachment prune")
		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("pruning attachments: %v", err)
		}
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		unreachable, err := findUnreachableAttachmentFiles(rootCtx, store)
		if err != nil {
			FatalErrorRespectJSON("pruning attachments: %v", err)
		}
		if !dryRun {
			for _, file := range unreachable {
				if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
					FatalErrorRespectJSON("removing %s: %v", file.StorageRelPath, err)
				}
			}
		}
		if jsonOutput {
			status := "pruned"
			if dryRun {
				status = "would_prune"
			}
			outputJSON(attachmentMaintenanceResult{
				Status:      status,
				Count:       len(unreachable),
				Unreachable: unreachable,
				DryRun:      dryRun,
			})
			return
		}
		if len(unreachable) == 0 {
			fmt.Println("No unreachable attachment files found")
			return
		}
		verb := "Removed"
		if dryRun {
			verb = "Would remove"
		}
		for _, file := range unreachable {
			fmt.Printf("%s %s %s\n", ui.RenderPass("✓"), verb, file.StorageRelPath)
		}
	},
}

func attachmentListEntries(st any, attachments []*types.Attachment) []attachmentListItem {
	if attachments == nil {
		return []attachmentListItem{}
	}
	items := make([]attachmentListItem, 0, len(attachments))
	for _, attachment := range attachments {
		items = append(items, attachmentListEntry(st, attachment))
	}
	return items
}

func attachmentListEntry(st any, attachment *types.Attachment) attachmentListItem {
	if attachment == nil {
		return attachmentListItem{}
	}
	return attachmentListItem{
		ID:               attachment.ID,
		IssueID:          attachment.IssueID,
		HashAlgorithm:    attachment.HashAlgorithm,
		ContentHash:      attachment.ContentHash,
		ShortHash:        shortAttachmentHash(attachment.ContentHash),
		OriginalFilename: attachment.OriginalFilename,
		MimeType:         attachment.MimeType,
		ByteSize:         attachment.ByteSize,
		Size:             formatBytes(attachment.ByteSize),
		StorageRelPath:   attachment.StorageRelPath,
		Missing:          !attachfs.Exists(st, attachment),
		CreatedBy:        attachment.CreatedBy,
		CreatedAt:        attachment.CreatedAt,
	}
}

func shortAttachmentHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func attachmentRelPathReferenced(ctx context.Context, st interface {
	ListAttachments(context.Context, string) ([]*types.Attachment, error)
}, issueID, relPath, exceptID string) bool {
	attachments, err := st.ListAttachments(ctx, issueID)
	if err != nil {
		return true
	}
	for _, attachment := range attachments {
		if attachment == nil || attachment.ID == exceptID {
			continue
		}
		if attachment.StorageRelPath == relPath {
			return true
		}
	}
	return false
}

func findUnreachableAttachmentFiles(ctx context.Context, st interface {
	SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error)
	ListAttachments(context.Context, string) ([]*types.Attachment, error)
}) ([]unreachableAttachmentFile, error) {
	referenced, err := referencedAttachmentPaths(ctx, st)
	if err != nil {
		return nil, err
	}
	return scanUnreachableAttachmentFiles(st, referenced)
}

func referencedAttachmentPaths(ctx context.Context, st interface {
	SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error)
	ListAttachments(context.Context, string) ([]*types.Attachment, error)
}) (map[string]struct{}, error) {
	issues, err := st.SearchIssues(ctx, "", types.IssueFilter{Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	referenced := make(map[string]struct{})
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		attachments, err := st.ListAttachments(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("list attachments for %s: %w", issue.ID, err)
		}
		for _, attachment := range attachments {
			if attachment == nil {
				continue
			}
			relPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(attachment.StorageRelPath))))
			if relPath != "." && relPath != "" {
				referenced[relPath] = struct{}{}
			}
		}
	}
	return referenced, nil
}

func scanUnreachableAttachmentFiles(st any, referenced map[string]struct{}) ([]unreachableAttachmentFile, error) {
	root, err := attachfs.Root(st)
	if err != nil {
		return nil, err
	}
	beadsDir, err := attachfs.BeadsDir(st)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return []unreachableAttachmentFile{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat attachment root: %w", err)
	}

	var unreachable []unreachableAttachmentFile
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(beadsDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := referenced[rel]; ok {
			return nil
		}
		unreachable = append(unreachable, unreachableAttachmentFile{
			StorageRelPath: rel,
			Path:           path,
			ByteSize:       info.Size(),
			Size:           formatBytes(info.Size()),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan attachment files: %w", err)
	}
	sort.Slice(unreachable, func(i, j int) bool {
		return unreachable[i].StorageRelPath < unreachable[j].StorageRelPath
	})
	return unreachable, nil
}

func init() {
	attachmentCopyCmd.Flags().BoolP("force", "f", false, "Overwrite the target file if it exists")
	attachmentPruneCmd.Flags().BoolP("dry-run", "n", false, "Show unreachable files without removing them")

	attachmentAddCmd.ValidArgsFunction = issueIDCompletion
	attachmentListCmd.ValidArgsFunction = issueIDCompletion
	attachmentCopyCmd.ValidArgsFunction = issueIDCompletion
	attachmentRemoveCmd.ValidArgsFunction = issueIDCompletion

	attachmentCmd.AddCommand(attachmentAddCmd)
	attachmentCmd.AddCommand(attachmentListCmd)
	attachmentCmd.AddCommand(attachmentCopyCmd)
	attachmentCmd.AddCommand(attachmentRemoveCmd)
	attachmentCmd.AddCommand(attachmentFsckCmd)
	attachmentCmd.AddCommand(attachmentPruneCmd)
	rootCmd.AddCommand(attachmentCmd)
}
