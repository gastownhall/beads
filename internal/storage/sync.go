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
	PushFiltered      bool // The pushed tip withholds local rows (federation.exclude_types)
	PulledCommits     int
	PushedCommits     int
	Conflicts         []Conflict
	ConflictsResolved bool
	Error             error
	PushError         error // Non-fatal push error
}

// SyncStore provides sync operations with peers.
type SyncStore interface {
	Sync(ctx context.Context, peer string, strategy string) (*SyncResult, error)
	SyncStatus(ctx context.Context, peer string) (*SyncStatus, error)
}
