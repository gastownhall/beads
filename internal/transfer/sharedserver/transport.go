// Package sharedserver implements InboxTransport for Dolt shared-server mode.
//
// Delivery works by switching to the target database on the same Dolt SQL server
// and inserting directly into its beads_inbox table. This requires both the
// source and target databases to be accessible on the same server instance.
package sharedserver

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/transfer"
	"github.com/steveyegge/beads/internal/types"
)

// validDBName matches safe Dolt database identifiers.
// Only alphanumeric, underscore, and hyphen are allowed. Must start with
// alphanumeric or underscore.
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-]*$`)

// Transport delivers inbox items via cross-database INSERT on a shared Dolt server.
type Transport struct {
	db       *sql.DB // Connection pool to the shared Dolt server
	database string  // Current (source) database name for restore after USE
}

// NewTransport creates a shared-server inbox transport.
// db is the connection pool and database is the current database name
// (used to restore context after cross-database operations).
func NewTransport(db *sql.DB, database string) (*Transport, error) {
	if !validDBName.MatchString(database) {
		return nil, fmt.Errorf("invalid source database name %q", database)
	}
	return &Transport{db: db, database: database}, nil
}

// Send delivers items to the destination's inbox using a cross-database INSERT.
// All inserts are wrapped in an explicit transaction for atomicity — either all
// items are delivered or none are. Uses ON DUPLICATE KEY UPDATE for idempotent
// resends.
func (t *Transport) Send(ctx context.Context, dest transfer.Destination, items []*types.InboxItem) (int, error) {
	if !validDBName.MatchString(dest.Address) {
		return 0, fmt.Errorf("invalid target database name %q: must match [a-zA-Z0-9_-]", dest.Address)
	}

	for _, item := range items {
		if err := storage.ValidateInboxItem(item); err != nil {
			return 0, err
		}
	}

	conn, err := t.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	// Switch to target database; restore original on all exit paths
	if _, err := conn.ExecContext(ctx, "USE `"+dest.Address+"`"); err != nil {
		return 0, fmt.Errorf("cannot access target database %q: %w (is the Dolt server shared?)", dest.Address, err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "USE `"+t.database+"`")
	}()

	// Begin explicit transaction for atomicity (fixes partial-success bug)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	sent := 0
	for _, item := range items {
		labels := item.Labels
		if labels == "" {
			labels = "[]"
		}
		metadata := item.Metadata
		if metadata == "" {
			metadata = "{}"
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO beads_inbox (
				inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				title = VALUES(title),
				description = VALUES(description),
				priority = VALUES(priority),
				issue_type = VALUES(issue_type),
				status = VALUES(status),
				labels = VALUES(labels),
				metadata = VALUES(metadata),
				sender_ref = VALUES(sender_ref)
		`,
			item.InboxID,
			item.SenderProjectID,
			item.SenderIssueID,
			item.Title,
			item.Description,
			item.Priority,
			item.IssueType,
			item.Status,
			labels,
			metadata,
			item.SenderRef,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to send %s: %w", item.SenderIssueID, err)
		}
		sent++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing transaction: %w", err)
	}
	tx = nil // prevent deferred rollback

	// Stage only the beads_inbox table and commit (not -Am which sweeps all dirty state)
	if sent > 0 {
		msg := fmt.Sprintf("inbox: received %d issue(s) from %s", sent, items[0].SenderProjectID)
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('beads_inbox')"); err != nil {
			return sent, fmt.Errorf("staging beads_inbox failed: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
			// Non-fatal: items are in the working set even if commit fails
			return sent, nil
		}
	}

	return sent, nil
}

// ValidateDestination checks whether the target database is accessible
// on the shared Dolt server without sending any data.
func (t *Transport) ValidateDestination(ctx context.Context, dest transfer.Destination) error {
	if !validDBName.MatchString(dest.Address) {
		return fmt.Errorf("invalid target database name %q: must match [a-zA-Z0-9_-]", dest.Address)
	}

	conn, err := t.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "USE `"+dest.Address+"`"); err != nil {
		return fmt.Errorf("cannot access target database %q: %w", dest.Address, err)
	}
	_, _ = conn.ExecContext(ctx, "USE `"+t.database+"`")
	return nil
}
