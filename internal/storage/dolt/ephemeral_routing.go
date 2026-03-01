package dolt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsEphemeralID returns true if the ID belongs to an ephemeral issue.
func IsEphemeralID(id string) bool {
	return strings.Contains(id, "-wisp-")
}

// defaultInfraTypes are the built-in infrastructure types routed to the wisps table.
// Override via DB config "types.infra" or config.yaml types.infra.
// Unexported to prevent external mutation; use DefaultInfraTypes() for a safe copy.
var defaultInfraTypes = []string{"agent", "rig", "role", "message"}

// DefaultInfraTypes returns a copy of the built-in infrastructure types.
func DefaultInfraTypes() []string {
	out := make([]string, len(defaultInfraTypes))
	copy(out, defaultInfraTypes)
	return out
}

// defaultInfraSet is the set form of defaultInfraTypes for IsInfraType lookups.
var defaultInfraSet = func() map[string]bool {
	m := make(map[string]bool, len(defaultInfraTypes))
	for _, t := range defaultInfraTypes {
		m[t] = true
	}
	return m
}()

// IsInfraType returns true if the issue type is infrastructure.
// Uses the hardcoded defaults (agent, rig, role, message).
// Prefer IsInfraTypeCtx when a DoltStore is available for config-driven behavior.
func IsInfraType(t types.IssueType) bool {
	return defaultInfraSet[string(t)]
}

// IsInfraTypeCtx returns true if the issue type is infrastructure, using the
// configured infra types from DB config / config.yaml / defaults.
func (s *DoltStore) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}

// isActiveWisp checks if an issue ID exists in the wisps table.
// Returns false if the wisp was promoted/deleted or doesn't exist.
// Used by CRUD methods to decide whether to route to wisp tables or fall through
// to permanent tables (handles promoted wisps correctly).
//
// For IDs matching the -wisp- pattern, does a full row scan (fast path for
// auto-generated wisp IDs). For other IDs, uses a lightweight existence check
// to support ephemeral beads created with explicit IDs (GH#2053).
func (s *DoltStore) isActiveWisp(ctx context.Context, id string) bool {
	if IsEphemeralID(id) {
		wisp, _ := s.getWisp(ctx, id)
		return wisp != nil
	}
	// Fallback: check wisps table for ephemeral beads with explicit IDs.
	// Ephemeral beads created with --id=<custom> don't contain "-wisp-" in
	// their ID, but are still stored in the wisps table. Use a lightweight
	// existence check to avoid full row scan on every non-wisp lookup.
	return s.wispExists(ctx, id)
}

// wispExists checks if an ID exists in the wisps table using a lightweight query.
// Used as a fallback for ephemeral beads with explicit (non-wisp) IDs (GH#2053).
func (s *DoltStore) wispExists(ctx context.Context, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

// allEphemeral returns true if all IDs in the slice are ephemeral.
func allEphemeral(ids []string) bool {
	for _, id := range ids {
		if !IsEphemeralID(id) {
			return false
		}
	}
	return len(ids) > 0
}

// partitionIDs separates IDs into ephemeral and dolt groups based on ID pattern only.
// NOTE: This misses explicit-ID ephemerals (GH#2053). For correct routing, use
// partitionByWispStatus which checks the wisps table as source of truth.
func partitionIDs(ids []string) (ephIDs, doltIDs []string) {
	for _, id := range ids {
		if IsEphemeralID(id) {
			ephIDs = append(ephIDs, id)
		} else {
			doltIDs = append(doltIDs, id)
		}
	}
	return
}

// partitionByWispStatus separates IDs into wisp (ephemeral) and permanent groups,
// using the wisps table as source of truth. Unlike partitionIDs (which only checks
// the ID pattern), this correctly handles explicit-ID ephemerals (GH#2053).
func (s *DoltStore) partitionByWispStatus(ctx context.Context, ids []string) (wispIDs, permIDs []string) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Fast partition by ID pattern — handles -wisp- IDs correctly
	var patternWispIDs []string
	patternWispIDs, permIDs = partitionIDs(ids)

	// Verify wisp-pattern IDs actually exist in the wisps table (bd-ftc).
	// Promoted wisps have -wisp- in their ID but live in the issues table,
	// so pattern-based routing alone misroutes them.
	if len(patternWispIDs) > 0 {
		activeSet := s.batchWispExists(ctx, patternWispIDs)
		for _, id := range patternWispIDs {
			if activeSet[id] {
				wispIDs = append(wispIDs, id)
			} else {
				permIDs = append(permIDs, id)
			}
		}
	}

	// Check if any permanent IDs are actually explicit-ID wisps (GH#2053)
	if len(permIDs) == 0 {
		return
	}

	activeSet := s.batchWispExists(ctx, permIDs)
	if len(activeSet) == 0 {
		return
	}

	var realPerm []string
	for _, id := range permIDs {
		if activeSet[id] {
			wispIDs = append(wispIDs, id)
		} else {
			realPerm = append(realPerm, id)
		}
	}
	permIDs = realPerm
	return
}

// batchWispExists returns the set of IDs that exist in the wisps table.
// Used by partitionByWispStatus to detect explicit-ID ephemerals in a single query.
func (s *DoltStore) batchWispExists(ctx context.Context, ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	//nolint:gosec // G201: placeholders contains only ? markers
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ",")),
		args...)
	if err != nil {
		return nil // On error, assume no wisps (safe fallback)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			result[id] = true
		}
	}
	return result
}

// PromoteFromEphemeral copies an issue from the wisps table to the issues table,
// clearing the Ephemeral flag. Used by bd promote and mol squash to crystallize wisps.
//
// Uses direct SQL inserts to bypass IsEphemeralID routing, which would otherwise
// redirect label/dependency/event writes back to wisp tables.
func (s *DoltStore) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	issue, err := s.getWisp(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("wisp %s not found", id)
	}
	if err != nil {
		return wrapDBError("get wisp for promote", err)
	}
	if issue == nil {
		return fmt.Errorf("wisp %s not found", id)
	}

	// Clear ephemeral flag for persistent storage
	issue.Ephemeral = false

	// Create in issues table (bypasses ephemeral routing since Ephemeral=false)
	if err := s.CreateIssue(ctx, issue, actor); err != nil {
		return fmt.Errorf("failed to promote wisp to issues: %w", err)
	}

	// Copy labels directly to permanent labels table (bypass IsEphemeralID routing)
	labels, err := s.getWispLabels(ctx, id)
	if err != nil {
		return wrapQueryError("get wisp labels for promote", err)
	}
	for _, label := range labels {
		if _, err := s.execContext(ctx,
			`INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
			id, label); err != nil {
			return fmt.Errorf("failed to copy label %q: %w", label, err)
		}
	}

	// Copy dependencies directly to permanent dependencies table
	deps, err := s.getWispDependencyRecords(ctx, id)
	if err != nil {
		return wrapQueryError("get wisp dependencies for promote", err)
	}
	for _, dep := range deps {
		metadata := dep.Metadata
		if metadata == "" {
			metadata = "{}"
		}
		if _, err := s.execContext(ctx, `
			INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy, metadata, dep.ThreadID); err != nil {
			// Skip if target doesn't exist (external ref to other wisp)
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "foreign key") {
				continue
			}
			return fmt.Errorf("failed to copy dependency: %w", err)
		}
	}

	// Copy events via INSERT...SELECT (best-effort: log but don't fail promotion)
	if _, err := s.execContext(ctx, `
		INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy events (data may be lost): %v", id, err)
	}

	// Copy comments via INSERT...SELECT (best-effort: log but don't fail promotion)
	if _, err := s.execContext(ctx, `
		INSERT IGNORE INTO comments (issue_id, author, text, created_at)
		SELECT issue_id, author, text, created_at
		FROM wisp_comments WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy comments (data may be lost): %v", id, err)
	}

	// Delete from wisps table (and all wisp_* auxiliary tables)
	return s.deleteWisp(ctx, id)
}

// getWispDependencyRecords returns raw dependency records for a wisp from wisp_dependencies.
func (s *DoltStore) getWispDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}
