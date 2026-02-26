package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// migrationData holds all data extracted from the source database.
type migrationData struct {
	issues      []*types.Issue
	labelsMap   map[string][]string
	depsMap     map[string][]*types.Dependency
	eventsMap   map[string][]*types.Event
	commentsMap map[string][]*types.Comment
	config      map[string]string
	prefix      string
	issueCount  int
}

// findSQLiteDB looks for a SQLite .db file in the beads directory.
// Returns the path to the first .db file found, or empty string if none.
func findSQLiteDB(beadsDir string) string {
	// Check common names first
	for _, name := range []string{"beads.db", "issues.db"} {
		p := filepath.Join(beadsDir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	// Scan for any .db file
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") &&
			!strings.Contains(entry.Name(), "backup") {
			return filepath.Join(beadsDir, entry.Name())
		}
	}
	return ""
}

// parseNullTime parses a time string into *time.Time. Returns nil for empty strings.
func parseNullTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// importToDolt imports all data to Dolt, returning (imported, skipped, error)
func importToDolt(ctx context.Context, store *dolt.DoltStore, data *migrationData) (int, int, error) {
	// Set all config values first
	for key, value := range data.config {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return 0, 0, fmt.Errorf("failed to set config %s: %w", key, err)
		}
	}

	tx, err := store.UnderlyingDB().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	imported := 0
	skipped := 0
	seenIDs := make(map[string]bool)
	total := len(data.issues)

	for i, issue := range data.issues {
		if !jsonOutput && total > 100 && (i+1)%100 == 0 {
			fmt.Printf("  Importing issues: %d/%d\r", i+1, total)
		}

		if seenIDs[issue.ID] {
			skipped++
			continue
		}
		seenIDs[issue.ID] = true

		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}

		// Normalize metadata: nil/empty → "{}"
		metadataStr := "{}"
		if len(issue.Metadata) > 0 {
			metadataStr = string(issue.Metadata)
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO issues (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes,
				created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
				compaction_level, compacted_at, compacted_at_commit, original_size,
				sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
				mol_type, work_type, quality_score, source_system, source_repo, close_reason, closed_by_session,
				event_kind, actor, target, payload,
				await_type, await_id, timeout_ns, waiters,
				hook_bead, role_bead, agent_state, last_activity, role_type, rig,
				due_at, defer_until, metadata
				) VALUES (
					?, ?, ?, ?, ?, ?, ?,
					?, ?, ?, ?, ?,
					?, ?, ?, ?, ?, ?, ?,
					?, ?, ?, ?,
					?, ?, ?, ?, ?, ?,
					?, ?, ?, ?, ?, ?, ?,
					?, ?, ?, ?,
					?, ?, ?, ?,
					?, ?, ?, ?, ?, ?,
				?, ?, ?
			)
			ON DUPLICATE KEY UPDATE
				content_hash = VALUES(content_hash),
				title = VALUES(title),
				description = VALUES(description),
				design = VALUES(design),
				acceptance_criteria = VALUES(acceptance_criteria),
				notes = VALUES(notes),
				status = VALUES(status),
				priority = VALUES(priority),
				issue_type = VALUES(issue_type),
				assignee = VALUES(assignee),
				estimated_minutes = VALUES(estimated_minutes),
				created_at = VALUES(created_at),
				created_by = VALUES(created_by),
				owner = VALUES(owner),
				updated_at = VALUES(updated_at),
				closed_at = VALUES(closed_at),
				external_ref = VALUES(external_ref),
				spec_id = VALUES(spec_id),
				compaction_level = VALUES(compaction_level),
				compacted_at = VALUES(compacted_at),
				compacted_at_commit = VALUES(compacted_at_commit),
				original_size = VALUES(original_size),
				sender = VALUES(sender),
				ephemeral = VALUES(ephemeral),
				wisp_type = VALUES(wisp_type),
				pinned = VALUES(pinned),
				is_template = VALUES(is_template),
				crystallizes = VALUES(crystallizes),
				mol_type = VALUES(mol_type),
				work_type = VALUES(work_type),
				quality_score = VALUES(quality_score),
				source_system = VALUES(source_system),
				source_repo = VALUES(source_repo),
				close_reason = VALUES(close_reason),
				closed_by_session = VALUES(closed_by_session),
				event_kind = VALUES(event_kind),
				actor = VALUES(actor),
				target = VALUES(target),
				payload = VALUES(payload),
				await_type = VALUES(await_type),
				await_id = VALUES(await_id),
				timeout_ns = VALUES(timeout_ns),
				waiters = VALUES(waiters),
				hook_bead = VALUES(hook_bead),
				role_bead = VALUES(role_bead),
				agent_state = VALUES(agent_state),
				last_activity = VALUES(last_activity),
				role_type = VALUES(role_type),
				rig = VALUES(rig),
				due_at = VALUES(due_at),
				defer_until = VALUES(defer_until),
				metadata = VALUES(metadata)
		`,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
			issue.Status, issue.Priority, issue.IssueType, nullableString(issue.Assignee), nullableIntPtr(issue.EstimatedMinutes),
			issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullableStringPtr(issue.ExternalRef), issue.SpecID,
			issue.CompactionLevel, issue.CompactedAt, nullableStringPtr(issue.CompactedAtCommit), nullableInt(issue.OriginalSize),
			issue.Sender, issue.Ephemeral, issue.WispType, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
			issue.MolType, issue.WorkType, nullableFloat32Ptr(issue.QualityScore), issue.SourceSystem, issue.SourceRepo, issue.CloseReason, issue.ClosedBySession,
			issue.EventKind, issue.Actor, issue.Target, issue.Payload,
			issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONArray(issue.Waiters),
			issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
			issue.DueAt, issue.DeferUntil, metadataStr,
		)
		if err != nil {
			return imported, skipped, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}

		// Reconcile labels for deterministic re-imports.
		if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE issue_id = ?`, issue.ID); err != nil {
			return imported, skipped, fmt.Errorf("failed to reset labels for issue %s: %w", issue.ID, err)
		}

		// Insert labels.
		// Keep ON DUPLICATE as a defensive guard: if source labels contain duplicates
		// or this flow is later changed to skip the pre-delete step, import remains idempotent.
		for _, label := range issue.Labels {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO labels (issue_id, label)
				VALUES (?, ?)
				ON DUPLICATE KEY UPDATE label = VALUES(label)
			`, issue.ID, label); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to insert label %q for issue %s: %v\n", label, issue.ID, err)
			}
		}

		imported++
	}

	if !jsonOutput && total > 100 {
		fmt.Printf("  Importing issues: %d/%d\n", total, total)
	}

	issueIDs := orderedIssueIDs(data)

	// Reconcile relations for deterministic and idempotent re-imports.
	// This is source-authoritative for migrated issue IDs: the target relation set
	// is replaced with what is present in the source snapshot.
	for _, issueID := range issueIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ?`, issueID); err != nil {
			return imported, skipped, fmt.Errorf("failed to reset dependencies for issue %s: %w", issueID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, issueID); err != nil {
			return imported, skipped, fmt.Errorf("failed to reset events for issue %s: %w", issueID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE issue_id = ?`, issueID); err != nil {
			return imported, skipped, fmt.Errorf("failed to reset comments for issue %s: %w", issueID, err)
		}
	}

	// Import dependencies
	migratePrintProgress("Importing dependencies...")
	for _, issue := range data.issues {
		for _, dep := range issue.Dependencies {
			metadataStr := normalizeDependencyMetadata(dep.Metadata)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at, metadata, thread_id)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE
					type = VALUES(type),
					created_by = VALUES(created_by),
					created_at = VALUES(created_at),
					metadata = VALUES(metadata),
					thread_id = VALUES(thread_id)
			`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedBy, dep.CreatedAt, metadataStr, dep.ThreadID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to insert dependency %s -> %s: %v\n", dep.IssueID, dep.DependsOnID, err)
			}
		}
	}

	// Import events (includes comments)
	migratePrintProgress("Importing events...")
	eventCount := 0
	eventSkipped := 0
	for _, issueID := range issueIDs {
		events := data.eventsMap[issueID]
		for _, event := range events {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, issueID, event.EventType, event.Actor,
				nullableStringPtr(event.OldValue), nullableStringPtr(event.NewValue),
				nullableStringPtr(event.Comment), event.CreatedAt)
			if err == nil {
				eventCount++
			} else {
				eventSkipped++
				fmt.Fprintf(os.Stderr, "Warning: failed to insert event for issue %s: %v\n", issueID, err)
			}
		}
	}
	if !jsonOutput {
		if eventSkipped > 0 {
			fmt.Printf("  Imported %d events (%d skipped)\n", eventCount, eventSkipped)
		} else {
			fmt.Printf("  Imported %d events\n", eventCount)
		}
	}

	// Import comments from legacy comments table (if available).
	migratePrintProgress("Importing comments...")
	commentCount := 0
	commentSkipped := 0
	for _, issueID := range issueIDs {
		comments := data.commentsMap[issueID]
		for _, comment := range comments {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO comments (issue_id, author, text, created_at)
				VALUES (?, ?, ?, ?)
			`, issueID, comment.Author, comment.Text, comment.CreatedAt)
			if err == nil {
				commentCount++
			} else {
				commentSkipped++
				fmt.Fprintf(os.Stderr, "Warning: failed to insert comment for issue %s: %v\n", issueID, err)
			}
		}
	}
	if !jsonOutput {
		if commentSkipped > 0 {
			fmt.Printf("  Imported %d comments (%d skipped)\n", commentCount, commentSkipped)
		} else {
			fmt.Printf("  Imported %d comments\n", commentCount)
		}
	}

	if err := tx.Commit(); err != nil {
		return imported, skipped, fmt.Errorf("failed to commit: %w", err)
	}

	return imported, skipped, nil
}

// Migration output helpers

func migratePrintProgress(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", message)
	}
}

func migratePrintSuccess(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", ui.RenderPass("✓ "+message))
	}
}

func migratePrintWarning(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", ui.RenderWarn("Warning: "+message))
	}
}

// Helper functions for nullable values

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func nullableIntPtr(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

func nullableInt(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

func nullableFloat32Ptr(f *float32) interface{} {
	if f == nil {
		return nil
	}
	return *f
}

func normalizeDependencyMetadata(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func sqliteOptionalTextExpr(columns map[string]bool, column string, fallback string) string {
	if columns != nil && columns[column] {
		return fmt.Sprintf("COALESCE(%s, %s)", column, fallback)
	}
	return fallback
}

func normalizedJSONBytes(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return nil
	}
	return encoded
}

func orderedIssueIDs(data *migrationData) []string {
	seen := make(map[string]struct{}, len(data.issues))
	ids := make([]string, 0, len(data.issues))

	for _, issue := range data.issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		ids = append(ids, issue.ID)
	}
	return ids
}

// formatJSONArray formats a string slice as JSON (matches Dolt schema expectation)
func formatJSONArray(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}
	return string(data)
}
