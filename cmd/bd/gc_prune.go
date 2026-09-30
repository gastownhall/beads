package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// storeSizeBytes returns the approximate on-disk size of the active database,
// or -1 when that database cannot be measured. It deliberately does not fall
// back to StoreLocator.Path: both concrete locator paths can contain sibling
// databases whose activity is unrelated to the current GC operation.
func storeSizeBytes(ctx context.Context) int64 {
	return storeSizeBytesForStore(ctx, store)
}

func storeSizeBytesForStore(ctx context.Context, candidate storage.DoltStorage) int64 {
	sizer, ok := storage.UnwrapStore(candidate).(storage.ActiveDatabaseSizer)
	if !ok {
		return -1
	}
	size, err := sizer.ActiveDatabaseSize(ctx)
	if err != nil {
		return -1
	}
	return size
}

// pruneRemoteRefsForGC deletes cached remote-tracking refs ahead of a
// post-squash GC — they anchor the pre-squash chain, and with them in place
// DOLT_GC reclaims nothing on any workspace that has ever pushed or fetched
// (bd-agctw). Also returns tags, which anchor history the same way but are
// user-created and therefore only warned about. Failures are warnings: GC
// still runs, it just reclaims less.
func pruneRemoteRefsForGC(ctx context.Context) (pruned, tags []string) {
	pruner, ok := storage.UnwrapStore(store).(storage.RemoteRefPruner)
	if !ok {
		return nil, nil
	}
	var err error
	pruned, err = pruner.PruneRemoteRefs(ctx)
	if err != nil {
		WarnError("pruning remote-tracking refs before GC: %v (GC may reclaim little)", err)
	}
	tags, err = pruner.ListTags(ctx)
	if err != nil {
		WarnError("listing tags before GC: %v", err)
	}
	return pruned, tags
}

// listRemoteRefsAndTags is the read-only companion for dry runs and bd gc.
func listRemoteRefsAndTags(ctx context.Context) (refs, tags []string) {
	pruner, ok := storage.UnwrapStore(store).(storage.RemoteRefPruner)
	if !ok {
		return nil, nil
	}
	refs, _ = pruner.ListRemoteRefs(ctx)
	tags, _ = pruner.ListTags(ctx)
	return refs, tags
}

// printPruneReport prints the outcome of pruneRemoteRefsForGC (text mode only).
func printPruneReport(pruned, tags []string) {
	if len(pruned) > 0 {
		fmt.Printf("  Pruned %d remote-tracking ref(s): %s\n", len(pruned), strings.Join(pruned, ", "))
		fmt.Printf("  (local cache only — the next push/fetch re-creates them at the new tip)\n")
	}
	if len(tags) > 0 {
		fmt.Printf("  Warning: %d tag(s) still anchor old history: %s\n", len(tags), strings.Join(tags, ", "))
		fmt.Printf("  GC cannot reclaim commits reachable from tags; delete unwanted tags and re-run GC.\n")
	}
}

// Dolt GC pass kinds, reported as the "mode"/"gc_mode" JSON field.
const (
	gcModeGenerational = "generational"
	gcModeFull         = "full"
)

// runDoltGCPass runs the requested Dolt GC pass and reports which pass actually
// ran. Dolt GC is generational: a default pass only visits data written since
// the last GC, while a full pass also collects the old generation. A store that
// does not implement FullGarbageCollector degrades to a generational pass with
// a warning.
func runDoltGCPass(ctx context.Context, gc storage.GarbageCollector, full bool) (string, error) {
	if full {
		if fullGC, ok := gc.(storage.FullGarbageCollector); ok {
			return gcModeFull, fullGC.DoltGCFull(ctx)
		}
		WarnError("storage backend does not support a full GC; running a generational pass, " +
			"which cannot reclaim data an earlier GC moved to the old generation")
	}
	return gcModeGenerational, gc.DoltGC(ctx)
}

// runPostRewriteGC runs the GC that follows a history rewrite and reports which
// pass ran ("" when the backend has no GC at all). It is always a full pass:
// the rewrite orphans a commit chain that any earlier GC already promoted to
// the old generation, and a generational pass never revisits it. Failures are
// warnings — the rewrite itself already succeeded.
func runPostRewriteGC(ctx context.Context, op string) string {
	gc, ok := storage.UnwrapStore(store).(storage.GarbageCollector)
	if !ok {
		return ""
	}
	if !jsonOutput {
		fmt.Println("  Running full Dolt GC (all generations; can take minutes on large stores)...")
	}
	mode, err := runDoltGCPass(ctx, gc, true)
	if err != nil {
		WarnError("dolt gc after %s failed: %v", op, err)
	}
	return mode
}

const (
	// fullGCHintMinStoreBytes keeps the hint off small stores, where a few
	// reclaimed kilobytes are not worth a multi-minute full pass.
	fullGCHintMinStoreBytes = 64 << 20
	// fullGCHintMaxFreedFraction is what counts as "reclaimed ~nothing".
	fullGCHintMaxFreedFraction = 0.01
)

// suggestFullGC reports whether a completed generational GC pass freed so
// little of a non-trivial store that the caller should suggest --full.
// before/after are ActiveDatabaseSize measurements; negative means unmeasurable
// and disables the hint.
func suggestFullGC(before, after int64) bool {
	if before < 0 || after < 0 || before < fullGCHintMinStoreBytes {
		return false
	}
	freed := before - after
	if freed < 0 {
		freed = 0
	}
	return float64(freed) < fullGCHintMaxFreedFraction*float64(before)
}

// printFullGCHint explains why a default pass can reclaim nothing (text mode).
func printFullGCHint() {
	fmt.Println("  Tip: little space was reclaimed. A default Dolt GC never revisits data an")
	fmt.Println("  earlier GC moved to the old generation — run 'bd gc --full' to collect all")
	fmt.Println("  generations.")
}

// gcSizeLine formats a before/after size pair as "X → Y (freed Z)", or ""
// when either measurement failed.
func gcSizeLine(before, after int64) string {
	if before < 0 || after < 0 {
		return ""
	}
	freed := before - after
	if freed < 0 {
		freed = 0
	}
	return fmt.Sprintf("%s → %s (freed %s)", formatBytes(before), formatBytes(after), formatBytes(freed))
}

// addGCSizeJSON adds size measurements to a JSON output map (fields omitted
// when a measurement failed).
func addGCSizeJSON(m map[string]interface{}, before, after int64) {
	if before >= 0 {
		m["size_before_bytes"] = before
	}
	if after >= 0 {
		m["size_after_bytes"] = after
	}
	if before >= 0 && after >= 0 {
		freed := before - after
		if freed < 0 {
			freed = 0
		}
		m["freed_bytes"] = freed
	}
}
