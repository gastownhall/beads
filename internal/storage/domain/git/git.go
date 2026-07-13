package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	internalgit "github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func NewGitRepository(workDir string) domain.GitRepository {
	return &gitRepositoryImpl{workDir: workDir}
}

type gitRepositoryImpl struct {
	workDir string
}

var _ domain.GitRepository = (*gitRepositoryImpl)(nil)

func (r *gitRepositoryImpl) gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.workDir
	return cmd
}

func (r *gitRepositoryImpl) IsGitRepo(ctx context.Context) (bool, error) {
	if err := r.gitCmd(ctx, "rev-parse", "--git-dir").Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("git: IsGitRepo: %w", err)
	}
	return true, nil
}

func (r *gitRepositoryImpl) IsBareGitRepo(ctx context.Context) (bool, error) {
	out, err := r.gitCmd(ctx, "rev-parse", "--is-bare-repository").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("git: IsBareGitRepo: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// IsJujutsuRepo walks upward from workDir looking for a .jj directory.
// Mirrors the boundary rule in internal/git.GetJujutsuRoot: a .git directory
// found before .jj terminates the walk so a nested git repo does not inherit
// an ancestor JJ workspace.
func (r *gitRepositoryImpl) IsJujutsuRepo(ctx context.Context) (bool, error) {
	dir, err := filepath.Abs(r.workDir)
	if err != nil {
		return false, fmt.Errorf("git: IsJujutsuRepo: abs %s: %w", r.workDir, err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".jj")); err == nil && info.IsDir() {
			return true, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

func (r *gitRepositoryImpl) IsColocatedJJGit(ctx context.Context) (bool, error) {
	isJJ, err := r.IsJujutsuRepo(ctx)
	if err != nil || !isJJ {
		return false, err
	}
	return r.IsGitRepo(ctx)
}

func (r *gitRepositoryImpl) Init(ctx context.Context) error {
	out, err := r.gitCmd(ctx, "init").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: Init: %w: %s", err, bytes.TrimSpace(out))
	}
	internalgit.ResetCaches()
	return nil
}

func (r *gitRepositoryImpl) GetConfig(ctx context.Context, key string) (string, bool, error) {
	if key == "" {
		return "", false, fmt.Errorf("git: GetConfig: key must not be empty")
	}
	out, err := r.gitCmd(ctx, "config", "--get", key).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: GetConfig %s: %w", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

func (r *gitRepositoryImpl) SetConfig(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("git: SetConfig: key must not be empty")
	}
	out, err := r.gitCmd(ctx, "config", key, value).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: SetConfig %s: %w: %s", key, err, bytes.TrimSpace(out))
	}
	return nil
}

func (r *gitRepositoryImpl) GetRemoteURL(ctx context.Context, name string) (string, bool, error) {
	if name == "" {
		return "", false, fmt.Errorf("git: GetRemoteURL: name must not be empty")
	}
	out, err := r.gitCmd(ctx, "remote", "get-url", name).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: GetRemoteURL %s: %w", name, err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", false, nil
	}
	return url, true, nil
}

func (r *gitRepositoryImpl) ListRemoteNames(ctx context.Context) ([]string, error) {
	out, err := r.gitCmd(ctx, "remote").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("git: ListRemoteNames: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

func (r *gitRepositoryImpl) CurrentBranch(ctx context.Context) (string, error) {
	out, err := r.gitCmd(ctx, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", nil
		}
		return "", fmt.Errorf("git: CurrentBranch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *gitRepositoryImpl) BranchHasUpstream(ctx context.Context, branch string) (bool, error) {
	if branch == "" {
		return false, fmt.Errorf("git: BranchHasUpstream: branch must not be empty")
	}
	if err := r.gitCmd(ctx, "config", "--get", "branch."+branch+".remote").Run(); err != nil {
		return false, nil
	}
	if err := r.gitCmd(ctx, "config", "--get", "branch."+branch+".merge").Run(); err != nil {
		return false, nil
	}
	return true, nil
}

func (r *gitRepositoryImpl) Add(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("git: Add: at least one path required")
	}
	args := append([]string{"add", "--"}, paths...)
	out, err := r.gitCmd(ctx, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git: Add: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (r *gitRepositoryImpl) Commit(ctx context.Context, params domain.GitCommitParams) (domain.GitCommitResult, error) {
	if params.Message == "" {
		return domain.GitCommitResult{}, fmt.Errorf("git: Commit: Message must not be empty")
	}
	args := []string{}
	if params.SkipHooks {
		args = append(args, "-c", "core.hooksPath=")
	}
	args = append(args, "commit")
	if params.NoVerify {
		args = append(args, "--no-verify")
	}
	args = append(args, "-m", params.Message)
	out, err := r.gitCmd(ctx, args...).CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("nothing to commit")) || bytes.Contains(out, []byte("no changes added to commit")) {
			return domain.GitCommitResult{DidCommit: false, Output: out}, nil
		}
		return domain.GitCommitResult{Output: out}, fmt.Errorf("git: Commit: %w: %s", err, bytes.TrimSpace(out))
	}
	return domain.GitCommitResult{DidCommit: true, Output: out}, nil
}
