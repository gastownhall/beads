package storage

import (
	"context"
	"time"
)

// CommitInfo represents a version control commit.
type CommitInfo struct {
	Hash    string
	Author  string
	Email   string
	Date    time.Time
	Message string
}

// StatusEntry represents a changed table in the working set.
type StatusEntry struct {
	Table  string
	Status string // "new", "modified", "deleted"
}

// Status represents the current repository status.
type Status struct {
	Staged   []StatusEntry
	Unstaged []StatusEntry
}

// VersionControl provides branch, commit, merge, and status operations.
type VersionControl interface {
	Branch(ctx context.Context, name string) error
	Checkout(ctx context.Context, branch string) error
	CurrentBranch(ctx context.Context) (string, error)
	DeleteBranch(ctx context.Context, branch string) error
	ListBranches(ctx context.Context) ([]string, error)
	Commit(ctx context.Context, message string) error
	// CommitWithConfig is like Commit but includes the config table.
	// Use after intentional config writes (bd init, bd config set, bd rename-prefix).
	// GH#3216: bootstrap paths must use this to commit issue_prefix.
	CommitWithConfig(ctx context.Context, message string) error
	// CommitMergeResolution concludes a merge whose conflicts the operator
	// resolved with an explicit --strategy (bd vc merge / bd federation sync),
	// committing the resolved working set INCLUDING config. Plain Commit excludes
	// config (GH#2455), so a config-only resolution — routine now that kv.* user
	// data syncs through config — would be dropped, leaving the merge unconcluded
	// and re-wedging the next pull/sync (GH#2474).
	CommitMergeResolution(ctx context.Context, message string) error
	CommitPending(ctx context.Context, actor string) (bool, error)
	CommitExists(ctx context.Context, commitHash string) (bool, error)
	GetCurrentCommit(ctx context.Context) (string, error)
	Status(ctx context.Context) (*Status, error)
	Log(ctx context.Context, limit int) ([]CommitInfo, error)
	Merge(ctx context.Context, branch string) ([]Conflict, error)
	GetConflicts(ctx context.Context) ([]Conflict, error)
	ResolveConflicts(ctx context.Context, table string, strategy string) error
}
