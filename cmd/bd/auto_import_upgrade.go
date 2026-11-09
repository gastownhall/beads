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
// this fall back to the multi-call path. ImportJSONLData returns the issues
// it wrote and the ids it skipped because a retaining sweep retired them.
type jsonlImporter interface {
	ImportJSONLData(ctx context.Context, issues []*types.Issue, configEntries map[string]string, actor string) (int, []string, error)
}

// fallbackImporter is the function maybeAutoImportJSONL invokes for stores
// that do not implement jsonlImporter (server-mode dolt). It exists as a
// package-level variable so tests can substitute a counter and verify the
// top-level emptiness guard prevents the fallback path from running on a
// non-empty database.
//
// It receives the plan maybeAutoImportJSONL already parsed and screened
// (planRecoveryJSONLImport) — the fallback never re-parses the file, so it
// cannot import or quarantine a different snapshot than the one the screen
// classified (GH#4492 design §1).
//
// Production builds import the plan with conflict-skip (GH#3955): this is
// upgrade-recovery into an empty DB, so insert-if-new and UPSERT are
// equivalent on the legitimate path — but if the emptiness guard above ever
// regresses again (cf. PR #3630), conflict-skip makes the fallback a
// harmless no-op instead of clobbering live rows. Explicit `bd import`,
// `bd bootstrap`, and `bd init --from-jsonl` are unaffected and keep UPSERT.
var fallbackImporter = importRecoveredPlanConflictSkip

// importRecoveredPlanConflictSkip is the production fallbackImporter: the
// conflict-skip writer over an already-screened plan.
func importRecoveredPlanConflictSkip(ctx context.Context, store storage.DoltStorage, plan *recoveredJSONLPlan) (*importLocalResult, error) {
	return importRecoveredPlanWithOpts(ctx, store, plan, true)
}

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
// conflict-skip, which is insert-if-new rather than UPSERT. So if this
// guard ever regresses again (cf. PR #3630), a stale issues.jsonl can no
// longer be re-imposed on top of live Dolt rows — the worst case degrades
// to a harmless no-op instead of clobbering recent writes.
//
// Recovery imports skip invalid records instead of aborting on them
// (GH#4492): the file is parsed and screened once
// (planRecoveryJSONLImport), every record the writer would refuse is
// quarantined beside the source before any database write, and the valid
// rows (and memories) import. One stale row in a shared mirror therefore
// costs its own quarantine entry, not the whole machine's write path. A
// failed screen or quarantine write still stamps the attempt and warns,
// and the outer command proceeds: auto-import is best-effort.
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

	// Parse and screen the JSONL file, quarantining rejected records beside
	// the source, before either importer touches the database.
	plan, err := planRecoveryJSONLImport(ctx, s, jsonlPath)
	if err != nil {
		writeAutoImportStamp(beadsDir, info)
		fmt.Fprintf(os.Stderr, "warning: auto-import: failed to import %s: %v\n", jsonlPath, err)
		fmt.Fprintf(os.Stderr, "\nYour issues are still safe in %s.\n", jsonlPath)
		fmt.Fprintf(os.Stderr, "Try: bd init --from-jsonl   (re-initialize and import from the JSONL file)\n")
		fmt.Fprintf(os.Stderr, "If this persists, please report at https://github.com/gastownhall/beads/issues\n\n")
		return
	}
	if len(plan.Issues) == 0 && len(plan.Memories) == 0 && len(plan.Rejected) == 0 {
		return // nothing to import
	}

	// Prefer single-transaction import (embedded mode) to avoid
	// DOLT_COMMIT races with concurrent writers.
	if importer, ok := s.(jsonlImporter); ok {
		imported, retiredIDs, err := importer.ImportJSONLData(ctx, plan.Issues, plan.Memories, "auto-import")
		if err != nil {
			writeAutoImportStamp(beadsDir, info)
			fmt.Fprintf(os.Stderr, "warning: auto-import from %s failed: %v\n", jsonlPath, err)
			fmt.Fprintf(os.Stderr, "\nYour issues are still safe in %s.\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "Try: bd init --from-jsonl   (re-initialize and import from the JSONL file)\n")
			fmt.Fprintf(os.Stderr, "If this persists, please report at https://github.com/gastownhall/beads/issues\n\n")
			return
		}
		// A file whose every row was retired or screened out imported
		// nothing, but it was attempted: stamp it, or every command would
		// re-read it. commandDidWrite stays unset for a file that wrote
		// nothing to the database — a quarantine file or stamp alone is not
		// a database write (GH#4492 design §4).
		if imported > 0 || len(retiredIDs) > 0 || len(plan.Memories) > 0 || len(plan.Rejected) > 0 {
			writeAutoImportStamp(beadsDir, info)
		}
		if imported > 0 || len(retiredIDs) > 0 || len(plan.Memories) > 0 {
			// Signal PersistentPostRun to auto-commit (no explicit DOLT_COMMIT here).
			commandDidWrite.Store(true)
			fmt.Fprintf(os.Stderr, "auto-imported %d issues", imported)
			if len(plan.Memories) > 0 {
				fmt.Fprintf(os.Stderr, " and %d config entries", len(plan.Memories))
			}
			printAutoImportRetired(len(retiredIDs))
			printAutoImportQuarantined(len(plan.Rejected), plan.Quarantine)
			fmt.Fprintf(os.Stderr, " from %s\n", jsonlPath)
		}
		return
	}

	// Fallback for stores without a single-transaction importer.
	fmt.Fprintf(os.Stderr, "auto-importing %d bytes from %s into empty database...\n", info.Size(), jsonlPath)

	result, err := fallbackImporter(ctx, s, plan)
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
	if result.Issues > 0 || result.Memories > 0 || result.Retired > 0 || result.Invalid > 0 {
		writeAutoImportStamp(beadsDir, info)
	}

	fmt.Fprintf(os.Stderr, "auto-imported %d issues", result.Issues)
	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, " and %d memories", result.Memories)
	}
	printAutoImportRetired(result.Retired)
	printAutoImportQuarantined(result.Invalid, plan.Quarantine)
	fmt.Fprintf(os.Stderr, " from %s\n", jsonlPath)
}

// printAutoImportRetired continues an "auto-imported N issues" line with the
// rows the import skipped because a retaining sweep retired their ids (`bd
// prune/purge --retain-ids`), in bd import's words, so a file the operator may
// hold as the last copy of those rows is not reported as restored.
func printAutoImportRetired(retired int) {
	if retired > 0 {
		fmt.Fprintf(os.Stderr, " (%d retired by a retention sweep, not re-created)", retired)
	}
}

// printAutoImportQuarantined continues the line with the records the screen
// refused (GH#4492) and where their raw bytes were preserved, so a recovery
// import that skipped something never looks like a clean import. A count of
// zero prints nothing, keeping the ordinary recovery message unchanged.
func printAutoImportQuarantined(rejected int, quarantine string) {
	if rejected > 0 {
		fmt.Fprintf(os.Stderr, " (%d invalid records quarantined in %s)", rejected, quarantine)
	}
}
