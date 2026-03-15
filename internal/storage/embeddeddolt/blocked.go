//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// computeBlockedIDs returns the set of issue IDs that are blocked by active
// issues. The logic mirrors DoltStore.computeBlockedIDs but without caching
// (each call runs inside a short-lived withConn transaction).
//
// When includeWisps is true, the wisps and wisp_dependencies tables are also
// scanned. All tables are guaranteed to exist because initSchema applies all
// migrations before New() returns.
func computeBlockedIDs(ctx context.Context, tx *sql.Tx, includeWisps bool) ([]string, error) {
	issueTables := []string{"issues"}
	depTables := []string{"dependencies"}
	if includeWisps {
		issueTables = append(issueTables, "wisps")
		depTables = append(depTables, "wisp_dependencies")
	}

	activeIDs, err := getActiveIDs(ctx, tx, issueTables)
	if err != nil {
		return nil, err
	}

	allDeps, err := getBlockingDeps(ctx, tx, depTables)
	if err != nil {
		return nil, err
	}

	blockedSet, waitsForDeps, needsClosedChildren := classifyDeps(allDeps, activeIDs)

	if len(waitsForDeps) > 0 {
		if err := evaluateWaitsForGates(ctx, tx, waitsForDeps, needsClosedChildren, activeIDs, depTables, issueTables, blockedSet); err != nil {
			return nil, err
		}
	}

	return mapKeys(blockedSet), nil
}

// getActiveIDs returns IDs of all issues with status NOT IN (closed, pinned).
func getActiveIDs(ctx context.Context, tx *sql.Tx, issueTables []string) (map[string]bool, error) {
	activeIDs := make(map[string]bool)
	for _, table := range issueTables {
		//nolint:gosec // G201: table is hardcoded to "issues" or "wisps"
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT id FROM %s WHERE status NOT IN ('closed', 'pinned')`, table))
		if err != nil {
			return nil, fmt.Errorf("compute blocked IDs: active issues from %s: %w", table, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("compute blocked IDs: scan active issue: %w", err)
			}
			activeIDs[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("compute blocked IDs: active rows from %s: %w", table, err)
		}
	}
	return activeIDs, nil
}

// depRecord holds a single blocking dependency row.
type depRecord struct {
	issueID, dependsOnID, depType string
	metadata                      sql.NullString
}

// getBlockingDeps returns all dependencies of type blocks, waits-for, or
// conditional-blocks.
func getBlockingDeps(ctx context.Context, tx *sql.Tx, depTables []string) ([]depRecord, error) {
	var allDeps []depRecord
	for _, depTable := range depTables {
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, depends_on_id, type, metadata FROM %s
			 WHERE type IN ('blocks', 'waits-for', 'conditional-blocks')`, depTable))
		if err != nil {
			return nil, fmt.Errorf("compute blocked IDs: deps from %s: %w", depTable, err)
		}
		for rows.Next() {
			var rec depRecord
			if err := rows.Scan(&rec.issueID, &rec.dependsOnID, &rec.depType, &rec.metadata); err != nil {
				rows.Close()
				return nil, fmt.Errorf("compute blocked IDs: scan dep: %w", err)
			}
			allDeps = append(allDeps, rec)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("compute blocked IDs: dep rows from %s: %w", depTable, err)
		}
	}
	return allDeps, nil
}

// waitsForDep is a waits-for dependency edge pending gate evaluation.
type waitsForDep struct {
	issueID   string
	spawnerID string
	gate      string
}

// classifyDeps partitions deps into directly-blocked IDs and waits-for edges
// that need gate evaluation. Returns the blocked set, the waits-for edges,
// and whether any gate requires closed-child lookups.
func classifyDeps(allDeps []depRecord, activeIDs map[string]bool) (blockedSet map[string]bool, waitsFor []waitsForDep, needsClosedChildren bool) {
	blockedSet = make(map[string]bool)
	for _, rec := range allDeps {
		switch rec.depType {
		case string(types.DepBlocks), string(types.DepConditionalBlocks):
			if activeIDs[rec.issueID] && activeIDs[rec.dependsOnID] {
				blockedSet[rec.issueID] = true
			}
		case string(types.DepWaitsFor):
			if !activeIDs[rec.issueID] {
				continue
			}
			gate := types.ParseWaitsForGateMetadata(rec.metadata.String)
			if gate == types.WaitsForAnyChildren {
				needsClosedChildren = true
			}
			waitsFor = append(waitsFor, waitsForDep{
				issueID:   rec.issueID,
				spawnerID: rec.dependsOnID,
				gate:      gate,
			})
		}
	}
	return
}

// evaluateWaitsForGates loads spawner children and evaluates each waits-for
// gate, adding blocked issues to blockedSet.
func evaluateWaitsForGates(
	ctx context.Context, tx *sql.Tx,
	deps []waitsForDep, needsClosedChildren bool,
	activeIDs map[string]bool,
	depTables, issueTables []string,
	blockedSet map[string]bool,
) error {
	spawnerIDs := make(map[string]struct{})
	for _, dep := range deps {
		spawnerIDs[dep.spawnerID] = struct{}{}
	}

	spawnerChildren, childIDs, err := getSpawnerChildren(ctx, tx, spawnerIDs, depTables)
	if err != nil {
		return err
	}

	closedChildren := make(map[string]bool)
	if needsClosedChildren && len(childIDs) > 0 {
		closedChildren, err = getClosedChildren(ctx, tx, childIDs, issueTables)
		if err != nil {
			return err
		}
	}

	for _, dep := range deps {
		children := spawnerChildren[dep.spawnerID]
		switch dep.gate {
		case types.WaitsForAnyChildren:
			// Block only while spawned children are active and none have completed.
			if len(children) == 0 {
				continue
			}
			hasClosedChild := false
			hasActiveChild := false
			for _, childID := range children {
				if closedChildren[childID] {
					hasClosedChild = true
					break
				}
				if activeIDs[childID] {
					hasActiveChild = true
				}
			}
			if !hasClosedChild && hasActiveChild {
				blockedSet[dep.issueID] = true
			}
		default:
			// all-children / children-of: block while any child remains active.
			for _, childID := range children {
				if activeIDs[childID] {
					blockedSet[dep.issueID] = true
					break
				}
			}
		}
	}
	return nil
}

// getSpawnerChildren returns the child IDs for each spawner via parent-child
// dependencies, plus the full set of child IDs seen. Uses a batched IN query
// per dep table to avoid N+1 round-trips.
func getSpawnerChildren(ctx context.Context, tx *sql.Tx, spawnerIDs map[string]struct{}, depTables []string) (map[string][]string, map[string]struct{}, error) {
	spawnerChildren := make(map[string][]string)
	childIDs := make(map[string]struct{})
	if len(spawnerIDs) == 0 {
		return spawnerChildren, childIDs, nil
	}

	args := make([]any, 0, len(spawnerIDs))
	for id := range spawnerIDs {
		args = append(args, id)
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma

	for _, depTable := range depTables {
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, depends_on_id FROM %s
			 WHERE type = 'parent-child' AND depends_on_id IN (%s)`, depTable, placeholders), args...)
		if err != nil {
			return nil, nil, fmt.Errorf("compute blocked IDs: children from %s: %w", depTable, err)
		}
		for rows.Next() {
			var childID, spawnerID string
			if err := rows.Scan(&childID, &spawnerID); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("compute blocked IDs: scan child: %w", err)
			}
			spawnerChildren[spawnerID] = append(spawnerChildren[spawnerID], childID)
			childIDs[childID] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, fmt.Errorf("compute blocked IDs: child rows from %s: %w", depTable, err)
		}
	}
	return spawnerChildren, childIDs, nil
}

// getClosedChildren returns the subset of childIDs that have status "closed".
// Uses a batched IN query per issue table to avoid N+1 round-trips.
func getClosedChildren(ctx context.Context, tx *sql.Tx, childIDs map[string]struct{}, issueTables []string) (map[string]bool, error) {
	closed := make(map[string]bool)
	if len(childIDs) == 0 {
		return closed, nil
	}

	args := make([]any, 0, len(childIDs))
	for id := range childIDs {
		args = append(args, id)
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]

	for _, issueTbl := range issueTables {
		//nolint:gosec // G201: issueTbl is hardcoded to "issues" or "wisps"
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT id FROM %s WHERE id IN (%s) AND status = 'closed'`, issueTbl, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("compute blocked IDs: closed children from %s: %w", issueTbl, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("compute blocked IDs: scan closed child: %w", err)
			}
			closed[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("compute blocked IDs: closed child rows from %s: %w", issueTbl, err)
		}
	}
	return closed, nil
}

func mapKeys(m map[string]bool) []string {
	result := make([]string, 0, len(m))
	for id := range m {
		result = append(result, id)
	}
	return result
}
