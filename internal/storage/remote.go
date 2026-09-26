package storage

import "context"

// RemoteStore provides remote management and push/pull/fetch operations.
type RemoteStore interface {
	AddRemote(ctx context.Context, name, url string) error
	// AddRemoteWithRef is AddRemote for a git-backed remote whose Dolt data
	// lives on the git ref ref instead of DefaultGitDataRef. An empty ref is
	// AddRemote.
	AddRemoteWithRef(ctx context.Context, name, url, ref string) error
	RemoveRemote(ctx context.Context, name string) error
	HasRemote(ctx context.Context, name string) (bool, error)
	ListRemotes(ctx context.Context) ([]RemoteInfo, error)
	Push(ctx context.Context) error
	Pull(ctx context.Context) error
	ForcePush(ctx context.Context) error
	PushRemote(ctx context.Context, remote string, force bool) error
	PullRemote(ctx context.Context, remote string) error
	Fetch(ctx context.Context, peer string) error
	PushTo(ctx context.Context, peer string) error
	PullFrom(ctx context.Context, peer string) ([]Conflict, error)
}
