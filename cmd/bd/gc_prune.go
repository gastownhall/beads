package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// storeSizeBytes returns the on-disk size of the store's data directory, or
// -1 when it cannot be determined (no local path, walk error). Measured
// around DOLT_GC so a no-op reclaim is visible to the operator (bd-agctw).
func storeSizeBytes() int64 {
	loc, ok := storage.UnwrapStore(store).(storage.StoreLocator)
	if !ok || loc.Path() == "" {
		return -1
	}
	size, err := getDirSize(loc.Path())
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
