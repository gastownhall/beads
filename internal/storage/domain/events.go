package domain

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

type Event struct {
	IssueID  string
	Type     types.EventType
	Actor    string
	OldValue string
	NewValue string
	// Comment is free text attached to the event; empty records none.
	Comment string
}

type RecordEventOpts struct {
	UseWispsTable bool
}

type EventsSQLRepository interface {
	Record(ctx context.Context, evt Event, opts RecordEventOpts) error
	DeleteAllForIDs(ctx context.Context, ids []string, opts RecordEventOpts) (int, error)
	CountAllForIDs(ctx context.Context, ids []string, opts RecordEventOpts) (int, error)
}
