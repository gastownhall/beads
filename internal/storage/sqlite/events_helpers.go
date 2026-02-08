package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// recordCreatedEvent records a single creation event for an issue
func recordCreatedEvent(ctx context.Context, conn *sql.Conn, issue *types.Issue, actor string) error {
	eventData, err := json.Marshal(issue)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
	}
	eventDataStr := string(eventData)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, new_value)
		VALUES (?, ?, ?, ?)
	`, issue.ID, types.EventCreated, actor, eventDataStr)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}
	return nil
}

// recordCreatedEvents bulk records creation events for multiple issues
func recordCreatedEvents(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, new_value)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issue := range issues {
		eventData, err := json.Marshal(issue)
		if err != nil {
			// Fall back to minimal description if marshaling fails
			eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
		}

		_, err = stmt.ExecContext(ctx, issue.ID, types.EventCreated, actor, string(eventData))
		if err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}
	return nil
}
