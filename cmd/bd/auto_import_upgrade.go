package main

import (
	"context"
	"encoding/json"
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

// fallbackImporter is the function maybeAutoImportJSONL invokes for stores
// that do not implement jsonlImporter (server-mode dolt). It exists as a
// package-level variable so tests can substitute a counter and verify the
// top-level emptiness guard prevents the fallback path from running on a
// non-empty database.
//
// Production builds use importFromLocalJSONLConflictSkip (GH#3955): this is
// upgrade-recovery into an empty DB, so insert-if-new and UPSERT are
// equivalent on the legitimate path — but if the emptiness guard above ever
// regresses again (cf. PR #3630), conflict-skip makes the fallback a
// harmless no-op instead of clobbering live rows. Explicit `bd import`,
// `bd bootstrap`, and `bd init --from-jsonl` are unaffected and keep UPSERT.
var fallbackImporter = importFromLocalJSONLConflictSkip

type autoImportStamp struct {
	Size        int64 `json:"size"`
	ModTimeNano int64 `json:"mtime_ns"`
}

func autoImportStampPath(beadsDir string) string {
	return filepath.Join(beadsDir, ".auto-import-issues.jsonl")
}

func autoImportStampMatches(beadsDir string, info os.FileInfo) bool {
	data, err := os.ReadFile(autoImportStampPath(beadsDir))
	if err != nil {
		return false
	}
	var stamp autoImportStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return false
	}
	return stamp.Size == info.Size() && stamp.ModTimeNano == info.ModTime().UnixNano()
}

func writeAutoImportStamp(beadsDir string, info os.FileInfo) {
	stamp := autoImportStamp{Size: info.Size(), ModTimeNano: info.ModTime().UnixNano()}
	data, err := json.Marshal(stamp)
	if err != nil {
		return
	}
	_ = os.WriteFile(autoImportStampPath(beadsDir), data, 0o600)
}

// maybeAutoImportJSONL checks whether the database is empty and the configured
// import.path JSONL file exists in beadsDir. When both conditions are true it
// auto-imports the JSONL data so users upgrading from pre-0.56 (which used
// .beads/dolt/) to 1.0+ (which uses .beads/embeddeddolt/) don't appear to
// lose their issues.  See GH#2994.
//
// The top-level emptiness guard (GetStatistics) is the primary
// protection for BOTH the embedded fast-path and the server-mode
// fallback. Defense in depth backs each path up: the embedded
// jsonlImporter has its own in-transaction emptiness check (and is
// also insert-if-new, GH#3955), and the fallback path imports via
// importFromLocalJSONLConflictSkip, which is insert-if-new rather than
// UPSERT. So if this guard ever regresses again (cf. PR #3630), a stale
// issues.jsonl can no longer be re-imposed on top of live Dolt rows —
// the worst case degrades to a harmless no-op instead of clobbering
// recent writes.
//
// The function is best-effort: failures are logged as warnings but do not
// prevent the store from being used.
func maybeAutoImportJSONL(ctx context.Context, s storage.DoltStorage, beadsDir string) {
	// Quick check: does the JSONL file exist and have content?
	jsonlPath := configuredImportJSONLPath(beadsDir)
	info, err := os.Stat(jsonlPath)
	if err != nil || info.Size() == 0 {
		return // no JSONL file or empty — nothing to import
	}
	if autoImportStampMatches(beadsDir, info) {
		return // already attempted for this exact JSONL version — retry only when issues.jsonl changes
	}

	// Top-level emptiness guard (covers both embedded and fallback paths).
	// Without this, the fallback path silently re-imposes stale JSONL on
	// top of live Dolt rows via UPSERT semantics on every invocation.
	stats, err := s.GetStatistics(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-import: failed to check issue count: %v\n", err)
		return
	}
	if stats == nil {
		fmt.Fprintf(os.Stderr, "warning: auto-import: issue count unavailable\n")
		return
	}
	if stats.TotalIssues > 0 {
		return // database is not empty — nothing to do
	}

	// Parse the JSONL file without touching the store.
	issues, configEntries, err := parseJSONLFile(jsonlPath)
	if err != nil {
		writeAutoImportStamp(beadsDir, info)
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
			writeAutoImportStamp(beadsDir, info)
			fmt.Fprintf(os.Stderr, "warning: auto-import from %s failed: %v\n", jsonlPath, err)
			fmt.Fprintf(os.Stderr, "\nYour issues are still safe in %s.\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "Try: bd init --from-jsonl   (re-initialize and import from the JSONL file)\n")
			fmt.Fprintf(os.Stderr, "If this persists, please report at https://github.com/gastownhall/beads/issues\n\n")
			return
		}
		if imported > 0 {
			writeAutoImportStamp(beadsDir, info)
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

	// Fallback for stores without a single-transaction importer.
	fmt.Fprintf(os.Stderr, "auto-importing %d bytes from %s into empty database...\n", info.Size(), jsonlPath)

	result, err := fallbackImporter(ctx, s, jsonlPath)
	if err != nil {
		writeAutoImportStamp(beadsDir, info)
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
		writeAutoImportStamp(beadsDir, info)
		fmt.Fprintf(os.Stderr, "warning: auto-import: dolt commit failed: %v\n", err)
		return
	}
	if result.Issues > 0 || result.Memories > 0 {
		writeAutoImportStamp(beadsDir, info)
	}

	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, "auto-imported %d issues and %d memories from %s\n", result.Issues, result.Memories, jsonlPath)
	} else {
		fmt.Fprintf(os.Stderr, "auto-imported %d issues from %s\n", result.Issues, jsonlPath)
	}
}
