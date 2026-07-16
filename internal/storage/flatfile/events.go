package flatfile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// eventFilename returns the JSONL events file path for an issue.
func (s *FlatFileStore) eventFilename(issueID string) string {
	return filepath.Join(s.eventsDir, issueID+".jsonl")
}

// appendEvent appends a single event to the issue's JSONL events file,
// generating an ID and timestamp when absent. This is the flat-file
// counterpart of the events/wisp_events INSERTs in issueops.
func (s *FlatFileStore) appendEvent(ev *types.Event) error {
	if ev.ID == "" {
		ev.ID = issueops.NewEventID()
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	path, err := s.safeEventFilename(ev.IssueID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("flatfile: marshal event: %w", err)
	}
	data = append(data, '\n')
	// Journal the pre-image first so a failed transaction can restore
	// this events file to its prior contents.
	if err := s.journalPreImage(path); err != nil {
		return err
	}
	// If a crash mid-append left a torn final line without a newline,
	// start on a fresh line so the torn fragment corrupts only itself,
	// not this event too. The reader skips the fragment.
	if !fileEndsWithNewline(path) {
		data = append([]byte{'\n'}, data...)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("flatfile: open events for append: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("flatfile: append event: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flatfile: sync events: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("flatfile: close events: %w", err)
	}
	return nil
}

// recordEvent mirrors issueops.RecordEventInTable/RecordFullEventInTable:
// old_value and new_value are written explicitly (possibly empty, never NULL).
func (s *FlatFileStore) recordEvent(issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	return s.appendEvent(&types.Event{
		IssueID:   issueID,
		EventType: eventType,
		Actor:     actor,
		OldValue:  &oldValue,
		NewValue:  &newValue,
	})
}

// recordCommentEvent mirrors the label/comment event INSERTs, which leave
// old_value/new_value unwritten: NULL on durable issues (events table has no
// DEFAULT) but empty-string on wisps (wisp_events columns default to the
// empty string).
func (s *FlatFileStore) recordCommentEvent(issueID string, isWisp bool, eventType types.EventType, actor, comment string) error {
	ev := &types.Event{
		IssueID:   issueID,
		EventType: eventType,
		Actor:     actor,
		Comment:   &comment,
	}
	if isWisp {
		empty := ""
		ev.OldValue = &empty
		ev.NewValue = &empty
	}
	return s.appendEvent(ev)
}

// fileEndsWithNewline reports whether path's last byte is '\n'. Missing or
// empty files count as newline-terminated (nothing to heal). Read errors also
// return true: appendEvent will surface real I/O problems itself.
func fileEndsWithNewline(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], fi.Size()-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// safeEventFilename validates the issue ID before constructing the path.
func (s *FlatFileStore) safeEventFilename(issueID string) (string, error) {
	return safePath(s.eventsDir, issueID, ".jsonl")
}

// GetEvents returns audit events for an issue, newest first, capped at limit.
func (s *FlatFileStore) GetEvents(_ context.Context, issueID string, limit int) ([]*types.Event, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	events, err := s.readEventsFile(issueID)
	if err != nil {
		return nil, err
	}

	// Sort newest first. Stable so same-timestamp events keep JSONL append
	// order (true insertion order) instead of scrambling run-to-run.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt.After(events[j].CreatedAt)
	})

	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

// GetAllEventsSince returns all events across all issues since the given time.
func (s *FlatFileStore) GetAllEventsSince(_ context.Context, since time.Time) ([]*types.Event, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	entries, err := readDirSafe(s.eventsDir)
	if err != nil {
		return nil, fmt.Errorf("flatfile: readdir events: %w", err)
	}

	var result []*types.Event
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		issueID := strings.TrimSuffix(e.Name(), ".jsonl")
		// Stray files whose names are not valid issue IDs (e.g. macOS
		// AppleDouble "._TASKS-1.jsonl") are not ours: skip them instead
		// of failing the whole store-wide scan.
		if validateID(issueID) != nil {
			continue
		}
		events, err := s.readEventsFile(issueID)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			// Strict boundary, mirroring issueops' `created_at > ?`.
			if ev.CreatedAt.After(since) {
				result = append(result, ev)
			}
		}
	}

	// Stable: preserve per-file append order among equal timestamps.
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// CountEvents returns the count of events for an issue. Mirrors the SQL
// surface's intentional asymmetry (same as CountIssueComments): the count
// queries only the durable `events` table and is never wisp-routed, so a wisp
// always reports 0 even though GetEvents returns its events.
func (s *FlatFileStore) CountEvents(_ context.Context, issueID string, limit int) (int64, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}

	if issue, err := s.readIssue(issueID); err == nil && issueops.IsWisp(issue) {
		return 0, nil
	}

	events, err := s.readEventsFile(issueID)
	if err != nil {
		return 0, err
	}

	count := int64(len(events))
	if limit > 0 && count > int64(limit) {
		count = int64(limit)
	}
	return count, nil
}

// readEventsFile reads all events from a per-issue JSONL file.
func (s *FlatFileStore) readEventsFile(issueID string) ([]*types.Event, error) {
	path, err := s.safeEventFilename(issueID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("flatfile: open events: %w", err)
	}
	defer f.Close()

	// bufio.Reader instead of bufio.Scanner: events embed full issue
	// snapshots (description, design, notes), which routinely exceed
	// Scanner's 64KB default token limit; Reader has no line-length cap.
	var events []*types.Event
	r := bufio.NewReader(f)
	for lineNo := 1; ; lineNo++ {
		line, readErr := r.ReadBytes('\n')
		line = bytes.TrimSuffix(line, []byte("\n"))
		if len(line) > 0 {
			var ev types.Event
			if err := json.Unmarshal(line, &ev); err != nil {
				// Events are audit data: tolerate a corrupt line (torn
				// append after a crash) so the readable events survive,
				// mirroring loadAllIssues' skip-and-warn posture.
				fmt.Fprintf(os.Stderr, "warning: skipping corrupt event line %d in %s: %v\n", lineNo, path, err)
			} else {
				events = append(events, &ev)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("flatfile: scan events: %w", readErr)
		}
	}
	return events, nil
}
