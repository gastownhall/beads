package storage

import "context"

// RemoteStore provides remote management and push/pull/fetch operations.
type RemoteStore interface {
	AddRemote(ctx context.Context, name, url string) error
	RemoveRemote(ctx context.Context, name string) error
	HasRemote(ctx context.Context, name string) (bool, error)
	ListRemotes(ctx context.Context) ([]RemoteInfo, error)
	Push(ctx context.Context) error
	Pull(ctx context.Context) error
	ForcePush(ctx context.Context) error
	PushRemote(ctx context.Context, remote string, force bool) error
	PullRemote(ctx context.Context, remote string) error
	// RebaseRemote reconciles hierarchical child-ID collisions (two clones
	// independently assigning the same parent.N id) that a plain pull cannot
	// auto-merge: it fetches remote, renumbers the losing side's colliding
	// children to free ids, then completes the merge. localDominates=false
	// (remote-dominates, the default) renumbers the local children; true
	// renumbers the remote's. Returns a report of what was renumbered.
	RebaseRemote(ctx context.Context, remote string, localDominates bool) (*RebaseReport, error)
	Fetch(ctx context.Context, peer string) error
	PushTo(ctx context.Context, peer string) error
	PullFrom(ctx context.Context, peer string) ([]Conflict, error)
}
