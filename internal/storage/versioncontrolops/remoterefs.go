package versioncontrolops

import (
	"context"
	"fmt"
)

// ListRemoteRefs returns the names of all cached remote-tracking refs
// (e.g. "remotes/origin/main"), sorted by name.
func ListRemoteRefs(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM dolt_remote_branches ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list remote-tracking refs: %w", err)
	}
	defer rows.Close()

	var refs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan remote-tracking ref: %w", err)
		}
		refs = append(refs, name)
	}
	return refs, rows.Err()
}

// PruneRemoteRefs deletes every cached remote-tracking ref and returns the
// names deleted. After a history squash (Flatten/Compact) these refs still
// anchor the pre-squash commit chain, so DOLT_GC treats the entire old history
// as reachable and reclaims nothing (bd-agctw). Pruning is safe on a squashed
// workspace: the refs are local caches only — nothing is deleted on the remote
// itself — and the next push or fetch re-creates them at the new tip.
//
// On error, the returned slice holds the refs deleted before the failure.
func PruneRemoteRefs(ctx context.Context, db DBConn) ([]string, error) {
	refs, err := ListRemoteRefs(ctx, db)
	if err != nil {
		return nil, err
	}
	var pruned []string
	for _, name := range refs {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', '-r', ?)", name); err != nil {
			return pruned, fmt.Errorf("delete remote-tracking ref %s: %w", name, err)
		}
		pruned = append(pruned, name)
	}
	return pruned, nil
}

// ListTags returns the names of all Dolt tags, sorted by name. Tags anchor
// history the same way remote-tracking refs do, but they are user-created, so
// callers should surface them rather than delete them.
func ListTags(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT tag_name FROM dolt_tags ORDER BY tag_name")
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}
