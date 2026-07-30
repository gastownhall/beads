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

// ConflictFieldValue is one column of a conflicted row, presented per side.
// A nil pointer means the side has no value for the field: either the side
// deleted (or never had) the row, or the column itself is NULL there.
type ConflictFieldValue struct {
	Name   string  `json:"name"`
	Base   *string `json:"base"`
	Ours   *string `json:"ours"`
	Theirs *string `json:"theirs"`
}

// ConflictRow is one conflicted row of one table, presented as fields rather
// than as the raw base_/our_/their_ column soup of dolt_conflicts_<table>.
type ConflictRow struct {
	Table string `json:"table"`
	// Key identifies the row: the issue ID for the issues table, and a
	// best-effort identifying value for the others.
	Key string `json:"key"`
	// OurDiffType/TheirDiffType are dolt's own classification of the
	// row on each side ("added", "modified", "removed"); empty when the
	// conflict table does not carry them.
	OurDiffType   string `json:"our_diff_type,omitempty"`
	TheirDiffType string `json:"their_diff_type,omitempty"`
	// BaseExists/OurExists/TheirExists report whether the row is present
	// on that side of the merge. A false OurExists or TheirExists is a
	// delete/modify conflict; a false BaseExists is an add/add conflict.
	BaseExists  bool                 `json:"base_exists"`
	OurExists   bool                 `json:"our_exists"`
	TheirExists bool                 `json:"their_exists"`
	Fields      []ConflictFieldValue `json:"fields"`
}

// Differs reports whether ours and theirs disagree on this field.
func (f ConflictFieldValue) Differs() bool {
	if f.Ours == nil || f.Theirs == nil {
		return f.Ours != f.Theirs
	}
	return *f.Ours != *f.Theirs
}

// ConflictInspector is the optional, issue-oriented conflict surface behind
// `bd conflicts` (GH federation ask #3). It is implemented by the Dolt-backed
// stores only; callers must type-assert.
type ConflictInspector interface {
	// GetConflictRows returns the live conflicted rows of table, per field.
	GetConflictRows(ctx context.Context, table string) ([]ConflictRow, error)
	// ResolveConflictRows resolves individual conflicted rows of table by
	// key (issue ID) with strategy "ours" or "theirs", leaving every other
	// conflicted row untouched. It returns the number of rows resolved.
	ResolveConflictRows(ctx context.Context, table string, keys []string, strategy string) (int, error)
}

// ConstraintViolation is one table's outstanding post-merge constraint
// violations (dolt_constraint_violations).
type ConstraintViolation struct {
	Table string `json:"table"`
	Count int    `json:"count"`
}

// MergeBlockers is the merge state that blocks concluding a merge but never
// appears in dolt_conflicts: a schema conflict, or a constraint violation the
// auto-repair path (mergesettle.go) declined. Both can be outstanding while
// every ROW conflict is resolved, which used to make the commit fail with a
// raw dolt error and no guidance (wy-36ilm F12).
type MergeBlockers struct {
	// Merging reports dolt_merge_status.is_merging: a merge is open and
	// awaiting its commit.
	Merging bool `json:"merging"`
	// SchemaConflictTables are the tables in dolt_schema_conflicts.
	SchemaConflictTables []string `json:"schema_conflict_tables,omitempty"`
	// ConstraintViolations are the tables with outstanding violations.
	ConstraintViolations []ConstraintViolation `json:"constraint_violations,omitempty"`
}

// Blocked reports whether anything here would refuse a merge commit.
func (b MergeBlockers) Blocked() bool {
	return len(b.SchemaConflictTables) > 0 || len(b.ConstraintViolations) > 0
}

// MergeBlockerInspector is the optional companion to ConflictInspector: the
// non-row merge state (schema conflicts, constraint violations, whether a
// merge is even open). Implemented by the Dolt-backed stores only; callers
// must type-assert, and a backend without it simply reports nothing.
type MergeBlockerInspector interface {
	GetMergeBlockers(ctx context.Context) (MergeBlockers, error)
}

// StrategicMerger is the optional companion to VersionControl.Merge for
// `bd vc merge --strategy` (#4992): merging and resolving conflicts with an
// explicit "ours"/"theirs" strategy in one call, on a pinned session with
// Dolt's conflict-tolerant flags set, so real conflicts are resolved and
// committed instead of rejecting the implicit autocommit transaction before
// the strategy can ever be applied. Implemented by the Dolt-backed stores
// only; callers must type-assert.
type StrategicMerger interface {
	// MergeWithStrategy merges branch into the current branch. If the merge
	// produces conflicts, every conflicted table is resolved with strategy
	// ("ours" or "theirs") and the resolution is committed; a clean merge
	// commits normally. Returns the conflicts that were present (resolved) —
	// empty for a clean merge — and any error. An error means the merge was
	// aborted and the working set restored.
	MergeWithStrategy(ctx context.Context, branch, strategy string) ([]Conflict, error)
}

// StrategicPuller is the optional companion to VersionControl's Pull/
// PullRemote for `bd dolt pull --strategy` (#4992 part 2): when a pull's
// merge hits conflicts the auto-resolver (TryAutoResolveMergeConflicts)
// declines — the concurrent-edit narrowing from GH#4698, most commonly —
// resolving them with an explicit "ours"/"theirs" strategy instead of
// aborting for the operator to resolve with the raw dolt CLI. Implemented by
// the embedded store, whose Pull path funnels through a single choke point
// (versioncontrolops.MergeAndSettle) that a strategy passes through cleanly.
// Server-mode DoltStore's pull routes through several transports (including
// shelling out to the dolt CLI for git-protocol/credentialed/cloud-auth
// remotes) with materially different conflict semantics per route, so it does
// not implement this interface; callers must type-assert and report a clear
// "not supported" error rather than silently ignoring --strategy.
type StrategicPuller interface {
	// PullWithStrategy is Pull with the operator escape hatch described above.
	PullWithStrategy(ctx context.Context, strategy string) error
	// PullRemoteWithStrategy is PullRemote with the same escape hatch.
	PullRemoteWithStrategy(ctx context.Context, remote, strategy string) error
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
