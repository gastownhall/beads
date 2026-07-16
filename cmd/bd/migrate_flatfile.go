package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/flatfile"
	"github.com/steveyegge/beads/internal/types"
)

// flatfileMigrationGitignore is the .beads/.gitignore written by the forward
// migration. Unlike the fresh-init flat-file gitignore (init.go), it keeps
// the Dolt data and runtime patterns from doctor.GitignoreTemplate: the Dolt
// directories stay on disk until the user removes them, and the flat-file
// auto-commit runs 'git add .beads/' after every mutating command — without
// these patterns the first post-migration commit would bake the entire Dolt
// binary database into git history.
const flatfileMigrationGitignore = `local_metadata.json
*.tmp
merge_slot.json
repo_mtime.json

# Dolt leftovers from before 'bd migrate flatfile' — kept ignored so
# auto-commit never stages the old Dolt database or its runtime files.
# Safe to drop these patterns once the directories are removed.
dolt/
embeddeddolt/
proxieddb/
bd.sock
bd.sock.startlock
daemon.*
*.lock
dolt-server.pid
dolt-server.log
`

var migrateFlatfileCmd = &cobra.Command{
	Use:   "flatfile",
	Short: "Migrate between Dolt and flat-file storage",
	Long: `Migrate all issues, dependencies, comments, labels, metadata, and config
from the Dolt database to flat-file JSON storage (.beads/issues/*.json).

After migration, the Dolt database is no longer needed and can be removed.
The flat-file backend uses git for sync instead of Dolt remotes.

With --reverse, migrate a flat-file workspace back to embedded Dolt — the
escape hatch for teams that hit the flat-file scale or merge ceiling. The
same data is carried in both directions via the portable import path used
by 'bd import'. Audit events are not transferred in either direction (no
backend exposes an event-import API); each backend records a fresh created
event at migration time. The source backend's files are left in place so
either migration can be retried, and can be removed afterwards.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		reverse, _ := cmd.Flags().GetBool("reverse")

		if !dryRun {
			CheckReadonly("migrate flatfile")
		}

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return HandleError("no .beads directory found; run 'bd init' first")
		}

		if reverse {
			return runFlatfileReverseMigration(beadsDir, dryRun)
		}

		// Check we're not already flatfile.
		if flatfile.IsFlatFileBackend(beadsDir) {
			fmt.Println("Already using flat-file backend.")
			return nil
		}

		// Open current Dolt store in read-only mode to bypass schema migration.
		// The PersistentPreRun store may fail on schema skew, so we open our own.
		s := getStore()
		if s == nil {
			// PersistentPreRun failed (schema skew, etc.) — try read-only open.
			var err error
			s, err = newReadOnlyStoreFromConfig(context.Background(), beadsDir)
			if err != nil {
				return HandleError("no database connection: %v\nIs Dolt running? Try: BD_IGNORE_SCHEMA_SKEW=1 bd migrate flatfile", err)
			}
			defer func() { _ = s.Close() }()
		}
		ctx := context.Background()

		// Export all issues.
		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return HandleError("failed to list issues: %v", err)
		}
		fmt.Printf("Found %d issues to migrate.\n", len(issues))

		if dryRun {
			fmt.Println("Dry run — no changes made.")
			return nil
		}

		// Create flat-file store (creates dirs).
		ffStore, err := flatfile.NewFlatFileStore(beadsDir)
		if err != nil {
			return HandleError("failed to create flat-file store: %v", err)
		}

		// Any failure below aborts BEFORE metadata.json is flipped, so the
		// workspace keeps routing to Dolt and nothing is lost: a migration
		// that silently dropped issues, labels, dependencies, comments, or
		// config would print success and invite 'rm -rf .beads/dolt'.
		abortMigration := func(format string, args ...interface{}) error {
			_ = ffStore.Close()
			return HandleError("%s\n\nMigration aborted: the Dolt data is untouched and the workspace still routes to Dolt.\nFix the cause, remove the partial flat-file output, and re-run:\n  %s\n  bd migrate flatfile",
				fmt.Sprintf(format, args...), flatfileDataCleanup(beadsDir))
		}

		// Migrate issues.
		failed := 0
		for _, issue := range issues {
			// Hydrate labels.
			labels, err := s.GetLabels(ctx, issue.ID)
			if err != nil {
				return abortMigration("failed to load labels for %s: %v", issue.ID, err)
			}
			issue.Labels = labels

			// Hydrate dependencies.
			var depRecords []*types.Dependency
			if dqStore, ok := s.(interface {
				GetDependencyRecords(context.Context, string) ([]*types.Dependency, error)
			}); ok {
				depRecords, err = dqStore.GetDependencyRecords(ctx, issue.ID)
				if err != nil {
					return abortMigration("failed to load dependencies for %s: %v", issue.ID, err)
				}
			} else {
				// Build Dependency records from resolved issues.
				deps, err := s.GetDependencies(ctx, issue.ID)
				if err != nil {
					return abortMigration("failed to load dependencies for %s: %v", issue.ID, err)
				}
				for _, dep := range deps {
					depRecords = append(depRecords, &types.Dependency{
						IssueID:     issue.ID,
						DependsOnID: dep.ID,
						Type:        "blocks",
					})
				}
			}
			issue.Dependencies = depRecords

			if err := ffStore.CreateIssue(ctx, issue, "migrate"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to migrate %s: %v\n", issue.ID, err)
				failed++
			}
		}
		if failed > 0 {
			return abortMigration("%d of %d issues failed to migrate", failed, len(issues))
		}
		fmt.Printf("Migrated %d/%d issues.\n", len(issues), len(issues))

		// Migrate comments.
		commentCount := 0
		for _, issue := range issues {
			comments, err := s.GetIssueComments(ctx, issue.ID)
			if err != nil {
				return abortMigration("failed to load comments for %s: %v", issue.ID, err)
			}
			for _, c := range comments {
				// ImportComment (not ImportIssueComment) so the Dolt comment ID
				// survives: a later --reverse migration must round-trip losslessly.
				if err := ffStore.ImportComment(ctx, c); err != nil {
					return abortMigration("failed to migrate comment on %s: %v", c.IssueID, err)
				}
				commentCount++
			}
		}
		if commentCount > 0 {
			fmt.Printf("Migrated %d comments.\n", commentCount)
		}

		// Migrate config KV (issue_prefix, bd-remember memories, custom keys).
		allConfig, err := s.GetAllConfig(ctx)
		if err != nil {
			return abortMigration("failed to read config: %v", err)
		}
		if err := copyAllConfig(ctx, ffStore, allConfig); err != nil {
			return abortMigration("%v", err)
		}
		fmt.Printf("Migrated %d config entries.\n", len(allConfig))

		// Update metadata.json to flatfile backend. Read/parse failures abort
		// (see mutateMetadataFile) — and without the backend flip the
		// migration did NOT complete; a warning here would print success
		// while bd still routes to Dolt.
		if err := mutateMetadataFile(beadsDir, func(meta map[string]interface{}) {
			meta["backend"] = "flatfile"
			delete(meta, "dolt_mode")
			delete(meta, "dolt_database")
			if meta["project_id"] == nil {
				meta["project_id"] = configfile.GenerateProjectID()
			}
		}); err != nil {
			return abortMigration("failed to update metadata.json: %v", err)
		}

		// Update .gitignore for flat-file.
		gitignorePath := filepath.Join(beadsDir, ".gitignore")
		if err := os.WriteFile(gitignorePath, []byte(flatfileMigrationGitignore), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update .gitignore: %v\n", err)
		}

		_ = ffStore.Close()

		// Warn if issues would be gitignored after migration.
		if warning := flatfile.CheckGitignored(beadsDir); warning != "" {
			fmt.Fprintf(os.Stderr, "\n%s\n", warning)
		}

		fmt.Println("\nMigration complete. The flat-file backend is now active.")
		fmt.Println("You can remove the Dolt data directory to reclaim space:")
		fmt.Printf("  rm -rf %s/dolt %s/embeddeddolt\n", beadsDir, beadsDir)
		return nil
	},
}

// flatfileDataCleanup returns the rm -rf command covering every flat-file
// artifact the forward migration writes: issues/, comments/, events/
// (CreateIssue appends created and label_added events per issue), and
// config_kv.json. Shared by the forward abort advice and the reverse
// success message so the two lists cannot drift — an abort cleanup that
// missed events/ made the retry append a second created event per issue,
// permanently duplicating the audit trail.
func flatfileDataCleanup(beadsDir string) string {
	return fmt.Sprintf("rm -rf %s/issues %s/comments %s/events %s/config_kv.json",
		beadsDir, beadsDir, beadsDir, beadsDir)
}

// mutateMetadataFile rewrites .beads/metadata.json via read → mutate →
// atomic write. Read AND parse failures are fatal: rebuilding from an empty
// map would silently mint a new project_id and drop every other key
// (deletions_retention_days, global_dolt_database, ...) while the migration
// prints success — a silent project-identity change. The caller decides how
// to surface the error (the two migration directions leave the workspace
// routed to different backends at this point).
//
// atomicWriteFile (temp+rename) on the write side: a plain os.WriteFile
// truncates in place, so a crash or a concurrent configfile.Load mid-write
// could observe an empty/partial metadata.json and break backend routing.
func mutateMetadataFile(beadsDir string, mutate func(map[string]interface{})) error {
	metaPath := filepath.Join(beadsDir, "metadata.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("failed to read metadata.json: %w", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("metadata.json is not valid JSON (restore it from git or fix it by hand): %w", err)
	}
	mutate(meta)
	newMeta, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode metadata.json: %w", err)
	}
	newMeta = append(newMeta, '\n')
	return atomicWriteFile(metaPath, newMeta)
}

// copyAllConfig writes every source config KV entry into dst, failing on the
// first error. Both migration directions use it: a dropped key is data loss
// (issue_prefix drives ID generation; bd-remember memories have no other
// copy once the source backend's files are removed), so failure must abort
// the migration before the backend flip, never demote to a warning.
func copyAllConfig(ctx context.Context, dst interface {
	SetConfig(ctx context.Context, key, value string) error
}, allConfig map[string]string) error {
	for k, v := range allConfig {
		if err := dst.SetConfig(ctx, k, v); err != nil {
			return fmt.Errorf("failed to migrate config %s: %w", k, err)
		}
	}
	return nil
}

// runFlatfileReverseMigration converts a flat-file workspace back to embedded
// Dolt. The read side hydrates issues exactly as 'bd export' does (bulk label,
// dependency, and comment getters); the write side is importIssuesCore — the
// same batch import behind 'bd import' and bootstrap restore — so field
// fidelity is covered by the existing import/export round-trip guarantees
// instead of a bespoke copy loop. Config KV is copied in full (issue_prefix,
// memories, custom keys). Metadata slots ride the issues' metadata column.
// Audit events are not transferred: no backend exposes an event-import API
// (events are transaction-scoped side effects of mutations), and the forward
// migration does not carry them either.
func runFlatfileReverseMigration(beadsDir string, dryRun bool) error {
	if !flatfile.IsFlatFileBackend(beadsDir) {
		return HandleError("not using the flat-file backend; 'bd migrate flatfile --reverse' converts a flat-file workspace back to Dolt")
	}
	ctx := context.Background()

	// PersistentPreRun opened the flat-file store (metadata says flatfile).
	src := getStore()
	if src == nil {
		var err error
		src, err = flatfile.OpenStore(ctx, beadsDir)
		if err != nil {
			return HandleError("failed to open flat-file store: %v", err)
		}
		defer func() { _ = src.Close() }()
	}

	issues, err := src.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return HandleError("failed to list issues: %v", err)
	}

	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}
	labelsMap, err := src.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		return HandleError("failed to load labels: %v", err)
	}
	depsMap, err := src.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return HandleError("failed to load dependencies: %v", err)
	}
	commentsMap, err := src.GetCommentsForIssues(ctx, issueIDs)
	if err != nil {
		return HandleError("failed to load comments: %v", err)
	}
	commentCount := 0
	for _, issue := range issues {
		issue.Labels = labelsMap[issue.ID]
		issue.Dependencies = depsMap[issue.ID]
		issue.Comments = commentsMap[issue.ID]
		commentCount += len(issue.Comments)
		sanitizeZeroTime(issue)
	}

	allConfig, err := src.GetAllConfig(ctx)
	if err != nil {
		return HandleError("failed to read config: %v", err)
	}

	fmt.Printf("Found %d issues, %d comments, and %d config entries to migrate.\n",
		len(issues), commentCount, len(allConfig))
	if dryRun {
		fmt.Println("Dry run — no changes made.")
		return nil
	}

	// Derive the Dolt database name the way bd init does: an existing
	// metadata dolt_database wins, then the issue prefix, then the default.
	dbName := configfile.DefaultDoltDatabase
	if cfg, _ := configfile.Load(beadsDir); cfg != nil && cfg.DoltDatabase != "" {
		dbName = cfg.DoltDatabase
	} else if prefix := allConfig["issue_prefix"]; prefix != "" {
		if derived := dbNameFromPrefix(normalizeIssuePrefix(prefix)); dolt.ValidateDatabaseName(derived) == nil {
			dbName = derived
		}
	}

	// Open the embedded Dolt destination directly instead of routing through
	// metadata.json (newDoltStoreFromConfig), so the backend flip can happen
	// AFTER the import succeeds. Flipping first left a crash window: a
	// SIGKILL mid-import stranded metadata.json on backend=dolt over zero or
	// partial rows, with the intact flat-file data no longer routed to and
	// the --reverse re-run refused by the IsFlatFileBackend guard.
	dbName = sanitizeDBName(dbName)
	dst, err := openEmbeddedDoltForReverseMigration(ctx, beadsDir, dbName)
	if err != nil {
		return HandleError("failed to open Dolt store: %v", err)
	}

	// AllowStale: the flat-file data is the source of truth, even over rows
	// in a leftover pre-migration Dolt directory.
	result, err := importIssuesCore(ctx, "", dst, issues, ImportOptions{
		SkipPrefixValidation: true,
		AllowStale:           true,
	})
	if err != nil {
		_ = dst.Close()
		return HandleError("failed to import issues into Dolt: %v\nThe workspace still routes to flat-file; fix the cause and re-run 'bd migrate flatfile --reverse'.", err)
	}
	for _, skipped := range result.SkippedDependencies {
		fmt.Fprintf(os.Stderr, "Warning: skipped dependency %s\n", skipped)
	}
	fmt.Printf("Migrated %d/%d issues and %d comments.\n", result.Created, len(issues), commentCount)

	// Config-copy failure is fatal BEFORE the metadata flip, matching the
	// forward direction and the import-failure path above: config KV carries
	// issue_prefix (ID generation) and bd-remember memories, and the success
	// message advises deleting the flat-file config — demoting a failed key
	// to a warning would make that advice destroy the only copy.
	if err := copyAllConfig(ctx, dst, allConfig); err != nil {
		_ = dst.Close()
		return HandleError("%v\nThe workspace still routes to flat-file; fix the cause and re-run 'bd migrate flatfile --reverse'.", err)
	}
	fmt.Printf("Migrated %d config entries.\n", len(allConfig))

	if err := dst.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to close Dolt store: %v\n", err)
	}

	// All data is in Dolt; only now flip metadata.json to the Dolt backend.
	// Until this write lands the workspace keeps routing to flat-file, so a
	// crash anywhere above leaves it intact and the migration re-runnable
	// (importIssuesCore upserts, so a re-run over partial rows is safe).
	if err := mutateMetadataFile(beadsDir, func(meta map[string]interface{}) {
		meta["backend"] = configfile.BackendDolt
		meta["dolt_database"] = dbName
	}); err != nil {
		return HandleError("data imported into Dolt, but failed to update metadata.json: %v\nThe workspace still routes to flat-file; fix the cause and re-run 'bd migrate flatfile --reverse'.", err)
	}

	// Restore the Dolt-backend .gitignore (the flat-file one stopped ignoring
	// the Dolt data directories).
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(doctor.GitignoreTemplate), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update .gitignore: %v\n", err)
	}

	fmt.Println("\nMigration complete. The Dolt backend is now active.")
	fmt.Println("The flat-file data is no longer used and can be removed:")
	fmt.Printf("  %s\n", flatfileDataCleanup(beadsDir))
	return nil
}

func init() {
	migrateFlatfileCmd.Flags().Bool("dry-run", false, "Show what would be migrated without making changes")
	migrateFlatfileCmd.Flags().Bool("reverse", false, "Migrate a flat-file workspace back to embedded Dolt")
	migrateCmd.AddCommand(migrateFlatfileCmd)
}
