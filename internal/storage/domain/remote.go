package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

type DoltRemoteUseCase interface {
	CreateRemote(ctx context.Context, name, url string) error
	// CreateRemoteWithRef is CreateRemote for a git-backed remote whose Dolt
	// data lives on the git ref ref; an empty ref is CreateRemote. The ref is
	// checked against what the dolt argv boundary will accept, so a remote
	// cannot be recorded on a ref it could never be routed to.
	CreateRemoteWithRef(ctx context.Context, name, url, ref string) error
	UpdateRemote(ctx context.Context, name, url string) error
	DeleteRemote(ctx context.Context, name string) error
	ListRemotes(ctx context.Context) ([]Remote, error)
}

type Remote struct {
	Name string
	URL  string
	// Ref is the git ref a git-backed remote keeps its Dolt data on when one
	// was recorded; empty means Dolt's default, refs/dolt/data.
	Ref string
}

type RemoteSQLRepository interface {
	AddRemote(ctx context.Context, name, url string) error
	AddRemoteWithRef(ctx context.Context, name, url, ref string) error
	RemoveRemote(ctx context.Context, name string) error
	ListRemotes(ctx context.Context) ([]Remote, error)
}

func NewDoltRemoteUseCase(remoteRepo RemoteSQLRepository) DoltRemoteUseCase {
	return &doltRemoteUseCaseImpl{remoteRepo: remoteRepo}
}

type doltRemoteUseCaseImpl struct {
	remoteRepo RemoteSQLRepository
}

var _ DoltRemoteUseCase = (*doltRemoteUseCaseImpl)(nil)

func (u *doltRemoteUseCaseImpl) CreateRemote(ctx context.Context, name, url string) error {
	if name == "" {
		return fmt.Errorf("CreateRemote: name must not be empty")
	}
	if url == "" {
		return fmt.Errorf("CreateRemote: url must not be empty")
	}
	if err := u.remoteRepo.AddRemote(ctx, name, url); err != nil {
		return fmt.Errorf("CreateRemote %s: %w", name, err)
	}
	return nil
}

func (u *doltRemoteUseCaseImpl) CreateRemoteWithRef(ctx context.Context, name, url, ref string) error {
	if name == "" {
		return fmt.Errorf("CreateRemoteWithRef: name must not be empty")
	}
	if url == "" {
		return fmt.Errorf("CreateRemoteWithRef: url must not be empty")
	}
	// The SQL write paths below bind the ref as a parameter and would happily
	// record one the dolt argv boundary later refuses, which would leave a
	// remote that can be created but never pushed, diagnosed only at push time.
	// This is the write seam for new remotes, so the recordable ref set is
	// narrowed to the routable one here, beside the other argument checks.
	// UpdateRemote deliberately does not re-check: it carries and restores refs
	// that are already recorded, and must not become unable to put one back.
	if err := doltutil.ValidateGitDataRefArg(strings.TrimSpace(ref)); err != nil {
		return fmt.Errorf("CreateRemoteWithRef %s: invalid git data ref: %w", name, err)
	}
	if err := u.remoteRepo.AddRemoteWithRef(ctx, name, url, ref); err != nil {
		return fmt.Errorf("CreateRemoteWithRef %s: %w", name, err)
	}
	return nil
}

func (u *doltRemoteUseCaseImpl) UpdateRemote(ctx context.Context, name, url string) error {
	if name == "" {
		return fmt.Errorf("UpdateRemote: name must not be empty")
	}
	if url == "" {
		return fmt.Errorf("UpdateRemote: url must not be empty")
	}
	// Dolt has no atomic remote update, so this is remove-then-add. Capture
	// the old URL and ref first so a failed add can restore the remote
	// instead of leaving it deleted (bd-6dnrw.44 P3), and so a URL change
	// keeps the remote's git data ref. A listing that fails stops the update
	// before anything is removed: without it the ref could not be kept.
	remotes, err := u.remoteRepo.ListRemotes(ctx)
	if err != nil {
		return fmt.Errorf("UpdateRemote %s: list remotes before replacing: %w", name, err)
	}
	var oldURL, oldRef string
	for _, rem := range remotes {
		if rem.Name == name {
			oldURL = rem.URL
			oldRef = rem.Ref
			break
		}
	}
	// A git data ref belongs to git-backed remotes only; Dolt refuses it on
	// any other scheme, so a move to one drops it.
	newRef := oldRef
	if !doltutil.IsGitProtocolURL(url) {
		newRef = ""
	}
	if err := u.remoteRepo.RemoveRemote(ctx, name); err != nil {
		return fmt.Errorf("UpdateRemote %s: remove: %w", name, err)
	}
	if err := u.remoteRepo.AddRemoteWithRef(ctx, name, url, newRef); err != nil {
		if oldURL != "" {
			if restoreErr := u.remoteRepo.AddRemoteWithRef(ctx, name, oldURL, oldRef); restoreErr != nil {
				return fmt.Errorf("UpdateRemote %s: add: %w (restoring previous URL %s also failed: %v)", name, err, oldURL, restoreErr)
			}
			return fmt.Errorf("UpdateRemote %s: add: %w (previous URL %s restored)", name, err, oldURL)
		}
		return fmt.Errorf("UpdateRemote %s: add: %w", name, err)
	}
	return nil
}

func (u *doltRemoteUseCaseImpl) DeleteRemote(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("DeleteRemote: name must not be empty")
	}
	if err := u.remoteRepo.RemoveRemote(ctx, name); err != nil {
		return fmt.Errorf("DeleteRemote %s: %w", name, err)
	}
	return nil
}

func (u *doltRemoteUseCaseImpl) ListRemotes(ctx context.Context) ([]Remote, error) {
	remotes, err := u.remoteRepo.ListRemotes(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListRemotes: %w", err)
	}
	return remotes, nil
}
