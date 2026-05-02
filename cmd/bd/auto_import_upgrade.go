package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// jsonlImporter is implemented by stores that support single-transaction
// JSONL import (currently EmbeddedDoltStore). Stores that don't implement
// this fall back to the multi-call path.
type jsonlImporter interface {
	ImportJSONLData(ctx context.Context, issues []*types.Issue, configEntries map[string]string, actor string) (int, error)
}

// maybeAutoImportJSONL checks whether the database is empty and a
// issues.jsonl file exists in beadsDir. When both conditions are true it
// auto-imports the JSONL data so users upgrading from pre-0.56 (which used
// .beads/dolt/) to 1.0+ (which uses .beads/embeddeddolt/) don't appear to
// lose their issues.  See GH#2994.
//
// When the store implements jsonlImporter (embedded mode), the emptiness
// check and import happen in a single transaction with no DOLT_COMMIT —
// the caller's PersistentPostRun auto-commit handles the Dolt commit.
//
// The function is best-effort: failures are logged as warnings but do not
// prevent the store from being used.
func maybeAutoImportJSONL(ctx context.Context, s storage.DoltStorage, beadsDir string) {
	// Quick check: does the JSONL file exist and have content?
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	info, err := os.Stat(jsonlPath)
	if err != nil || info.Size() == 0 {
		return // no JSONL file or empty — nothing to import
	}

	// Parse the JSONL file without touching the store.
	issues, configEntries, err := parseJSONLFile(jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-import: failed to parse %s: %v\n", jsonlPath, err)
		return
	}
	if len(issues) == 0 {
		return // nothing to import
	}

	// Prefer single-transaction import (embedded mode) to avoid
	// DOLT_COMMIT races with concurrent writers.
	if importer, ok := s.(jsonlImporter); ok {
		imported, err := importer.ImportJSONLData(ctx, issues, configEntries, "auto-import")
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-import from %s failed: %v\n", jsonlPath, err)
			fmt.Fprintf(os.Stderr, "\nYour issues are still safe in %s.\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "Try: bd init --from-jsonl   (re-initialize and import from the JSONL file)\n")
			fmt.Fprintf(os.Stderr, "If this persists, please report at https://github.com/gastownhall/beads/issues\n\n")
			return
		}
		if imported > 0 {
			// Signal PersistentPostRun to auto-commit (no explicit DOLT_COMMIT here).
			commandDidWrite.Store(true)
			fmt.Fprintf(os.Stderr, "auto-imported %d issues", imported)
			if len(configEntries) > 0 {
				fmt.Fprintf(os.Stderr, " and %d config entries", len(configEntries))
			}
			fmt.Fprintf(os.Stderr, " from %s\n", jsonlPath)
		}
		return
	}

	// Fallback for non-embedded stores: multi-call path (original behavior).
	fmt.Fprintf(os.Stderr, "auto-importing %d bytes from %s into empty database...\n", info.Size(), jsonlPath)

	result, err := importFromLocalJSONLFull(ctx, s, jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-import from %s failed: %v\n", jsonlPath, err)
		fmt.Fprintf(os.Stderr, "\nYour issues are still safe in %s.\n", jsonlPath)
		fmt.Fprintf(os.Stderr, "Try: bd init --from-jsonl   (re-initialize and import from the JSONL file)\n")
		fmt.Fprintf(os.Stderr, "If this persists, please report at https://github.com/gastownhall/beads/issues\n\n")
		return
	}

	// Commit the imported data to Dolt history (fallback path only).
	commitMsg := fmt.Sprintf("auto-import: %d issues from %s (upgrade recovery, GH#2994)", result.Issues, filepath.Base(jsonlPath))
	if result.Memories > 0 {
		commitMsg = fmt.Sprintf("auto-import: %d issues, %d memories from %s (upgrade recovery, GH#2994)", result.Issues, result.Memories, filepath.Base(jsonlPath))
	}
	if err := s.Commit(ctx, commitMsg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-import: dolt commit failed: %v\n", err)
		return
	}

	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, "auto-imported %d issues and %d memories from %s\n", result.Issues, result.Memories, jsonlPath)
	} else {
		fmt.Fprintf(os.Stderr, "auto-imported %d issues from %s\n", result.Issues, jsonlPath)
	}
}
