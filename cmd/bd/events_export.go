package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/steveyegge/beads/internal/storage"
)

// exportEventsToJSONL appends new events to the events JSONL file.
// It reads the last exported event ID from metadata, fetches all events since then,
// appends them as JSON lines, and updates the metadata with the new high-water mark.
func exportEventsToJSONL(ctx context.Context, store storage.Storage, eventsPath string) error {
	// Read last exported event ID from metadata
	var sinceID int64
	lastIDStr, err := store.GetMetadata(ctx, "events_last_exported_id")
	if err == nil && lastIDStr != "" {
		sinceID, err = strconv.ParseInt(lastIDStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid events_last_exported_id metadata %q: %w", lastIDStr, err)
		}
	}

	// Fetch all events since the last exported ID
	events, err := store.GetAllEventsSince(ctx, sinceID)
	if err != nil {
		return fmt.Errorf("failed to get events since ID %d: %w", sinceID, err)
	}

	if len(events) == 0 {
		return nil // Nothing new to export
	}

	// Open file for appending (create if it doesn't exist)
	// #nosec G304 - controlled path from config
	// nolint:gosec // G302: JSONL needs to be readable by other tools
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open events file: %w", err)
	}
	defer f.Close()

	// Append each event as a JSON line
	encoder := json.NewEncoder(f)
	var maxID int64
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("failed to encode event %d: %w", event.ID, err)
		}
		if event.ID > maxID {
			maxID = event.ID
		}
	}

	// Update metadata with the new high-water mark
	if err := store.SetMetadata(ctx, "events_last_exported_id", strconv.FormatInt(maxID, 10)); err != nil {
		return fmt.Errorf("failed to update events_last_exported_id: %w", err)
	}

	return nil
}

// resetEventsExport resets the events export state by clearing the metadata
// and truncating the events JSONL file. Used with --events-reset flag.
func resetEventsExport(ctx context.Context, store storage.Storage, eventsPath string) error {
	// Clear the high-water mark
	if err := store.SetMetadata(ctx, "events_last_exported_id", ""); err != nil {
		return fmt.Errorf("failed to clear events_last_exported_id: %w", err)
	}

	// Truncate the events file if it exists
	if _, err := os.Stat(eventsPath); err == nil {
		if err := os.Truncate(eventsPath, 0); err != nil {
			return fmt.Errorf("failed to truncate events file: %w", err)
		}
	}

	return nil
}
