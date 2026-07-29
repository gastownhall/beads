package domain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type GitRepository interface {
	IsGitRepo(ctx context.Context) (bool, error)
	IsBareGitRepo(ctx context.Context) (bool, error)
	IsJujutsuRepo(ctx context.Context) (bool, error)
	IsColocatedJJGit(ctx context.Context) (bool, error)

	Init(ctx context.Context) error

	GetConfig(ctx context.Context, key string) (value string, found bool, err error)
	SetConfig(ctx context.Context, key, value string) error

	GetRemoteURL(ctx context.Context, name string) (url string, found bool, err error)
	ListRemoteNames(ctx context.Context) ([]string, error)

	CurrentBranch(ctx context.Context) (string, error)
	BranchHasUpstream(ctx context.Context, branch string) (bool, error)

	Add(ctx context.Context, paths ...string) error
	Commit(ctx context.Context, params GitCommitParams) (GitCommitResult, error)
}

type GitCommitParams struct {
	Message   string
	NoVerify  bool
	SkipHooks bool
}

type GitCommitResult struct {
	DidCommit bool
	Output    []byte
}

type GitUseCase interface {
	IsGitRepo(ctx context.Context) bool
	IsBareGitRepo(ctx context.Context) bool
	IsJujutsuRepo(ctx context.Context) bool
	IsColocatedJJGit(ctx context.Context) bool

	EnsureGitRepo(ctx context.Context) (EnsureGitRepoResult, error)
	OriginRemoteURL(ctx context.Context) (string, error)
	DetectFork(ctx context.Context) (isFork bool, upstreamURL string, err error)

	BeadsRole(ctx context.Context) (role string, hasRole bool, err error)
	SetBeadsRole(ctx context.Context, role string) error

	HasAnyRemotes(ctx context.Context) bool
	HasUpstream(ctx context.Context) bool

	CommitInitArtifacts(ctx context.Context, params CommitInitArtifactsParams) (CommitInitArtifactsResult, error)
}

type EnsureGitRepoResult struct {
	DidInit       bool
	AlreadyExists bool
}

type CommitInitArtifactsParams struct {
	BeadsDir      string
	OptionalPaths []string
	Message       string
	NoVerify      bool
	SkipHooks     bool
}

type CommitInitArtifactsResult struct {
	StagedPaths []string
	DidCommit   bool
}

const beadsRoleConfigKey = "beads.role"

func NewGitUseCase(workDir string, repo GitRepository) GitUseCase {
	return &gitUseCaseImpl{workDir: workDir, repo: repo}
}

type gitUseCaseImpl struct {
	workDir string
	repo    GitRepository
}

var _ GitUseCase = (*gitUseCaseImpl)(nil)

func (u *gitUseCaseImpl) IsGitRepo(ctx context.Context) bool {
	ok, _ := u.repo.IsGitRepo(ctx)
	return ok
}

func (u *gitUseCaseImpl) IsBareGitRepo(ctx context.Context) bool {
	ok, _ := u.repo.IsBareGitRepo(ctx)
	return ok
}

func (u *gitUseCaseImpl) IsJujutsuRepo(ctx context.Context) bool {
	ok, _ := u.repo.IsJujutsuRepo(ctx)
	return ok
}

func (u *gitUseCaseImpl) IsColocatedJJGit(ctx context.Context) bool {
	ok, _ := u.repo.IsColocatedJJGit(ctx)
	return ok
}

func (u *gitUseCaseImpl) EnsureGitRepo(ctx context.Context) (EnsureGitRepoResult, error) {
	exists, err := u.repo.IsGitRepo(ctx)
	if err != nil {
		return EnsureGitRepoResult{}, fmt.Errorf("EnsureGitRepo: check: %w", err)
	}
	if exists {
		return EnsureGitRepoResult{AlreadyExists: true}, nil
	}
	if err := u.repo.Init(ctx); err != nil {
		return EnsureGitRepoResult{}, fmt.Errorf("EnsureGitRepo: init: %w", err)
	}
	return EnsureGitRepoResult{DidInit: true}, nil
}

func (u *gitUseCaseImpl) OriginRemoteURL(ctx context.Context) (string, error) {
	isRepo, err := u.repo.IsGitRepo(ctx)
	if err != nil || !isRepo {
		return "", err
	}
	bare, err := u.repo.IsBareGitRepo(ctx)
	if err != nil || bare {
		return "", err
	}
	url, _, err := u.repo.GetRemoteURL(ctx, "origin")
	return url, err
}

func (u *gitUseCaseImpl) DetectFork(ctx context.Context) (bool, string, error) {
	isRepo, err := u.repo.IsGitRepo(ctx)
	if err != nil || !isRepo {
		return false, "", err
	}
	url, found, err := u.repo.GetRemoteURL(ctx, "upstream")
	if err != nil || !found {
		return false, "", err
	}
	return true, url, nil
}

func (u *gitUseCaseImpl) BeadsRole(ctx context.Context) (string, bool, error) {
	return u.repo.GetConfig(ctx, beadsRoleConfigKey)
}

func (u *gitUseCaseImpl) SetBeadsRole(ctx context.Context, role string) error {
	if role == "" {
		return fmt.Errorf("SetBeadsRole: role must not be empty")
	}
	return u.repo.SetConfig(ctx, beadsRoleConfigKey, role)
}

func (u *gitUseCaseImpl) HasAnyRemotes(ctx context.Context) bool {
	names, err := u.repo.ListRemoteNames(ctx)
	if err != nil {
		return false
	}
	return len(names) > 0
}

func (u *gitUseCaseImpl) HasUpstream(ctx context.Context) bool {
	branch, err := u.repo.CurrentBranch(ctx)
	if err != nil || branch == "" {
		return false
	}
	ok, err := u.repo.BranchHasUpstream(ctx, branch)
	if err != nil {
		return false
	}
	return ok
}

func (u *gitUseCaseImpl) CommitInitArtifacts(ctx context.Context, params CommitInitArtifactsParams) (CommitInitArtifactsResult, error) {
	if params.BeadsDir == "" {
		return CommitInitArtifactsResult{}, fmt.Errorf("CommitInitArtifacts: BeadsDir must not be empty")
	}
	if params.Message == "" {
		return CommitInitArtifactsResult{}, fmt.Errorf("CommitInitArtifacts: Message must not be empty")
	}

	paths := []string{params.BeadsDir}
	for _, p := range params.OptionalPaths {
		if p == "" {
			continue
		}
		statPath := p
		if !filepath.IsAbs(statPath) {
			statPath = filepath.Join(u.workDir, statPath)
		}
		if _, err := os.Stat(statPath); err == nil {
			paths = append(paths, p)
		}
	}

	if err := u.repo.Add(ctx, paths...); err != nil {
		return CommitInitArtifactsResult{}, fmt.Errorf("CommitInitArtifacts: add: %w", err)
	}

	commit, err := u.repo.Commit(ctx, GitCommitParams{
		Message:   params.Message,
		NoVerify:  params.NoVerify,
		SkipHooks: params.SkipHooks,
	})
	if err != nil {
		return CommitInitArtifactsResult{}, fmt.Errorf("CommitInitArtifacts: commit: %w", err)
	}
	return CommitInitArtifactsResult{StagedPaths: paths, DidCommit: commit.DidCommit}, nil
}
