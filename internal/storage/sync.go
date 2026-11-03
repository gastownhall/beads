package storage

import (
	"context"
	"time"
)

// SyncResult contains the outcome of a Sync operation.
type SyncResult struct {
	Peer              string
	StartTime         time.Time
	EndTime           time.Time
	Fetched           bool
	Merged            bool
	Pushed            bool
	PulledCommits     int
	PushedCommits     int
	Conflicts         []Conflict
	ConflictsResolved bool
	Error             error
	PushError         error // Non-fatal push error
	// PushFiltered is true when the successful publication actually
	// withheld rows — excluded issue types were filtered out of the pushed
	// tip. It says nothing about why or what: the excluded rows stay in the
	// sender's history and on peers that already held them, and the CLI
	// renders the history-visibility notice from this flag.
	PushFiltered bool
}

// SyncStore provides sync operations with peers.
type SyncStore interface {
	Sync(ctx context.Context, peer string, strategy string) (*SyncResult, error)
	SyncStatus(ctx context.Context, peer string) (*SyncStatus, error)
}
