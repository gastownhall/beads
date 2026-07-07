package git

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	domaingit "github.com/steveyegge/beads/internal/storage/domain/git"
)

type GitProvider interface {
	GitUseCase() domain.GitUseCase
}

func NewGitProvider(workDir string) GitProvider {
	return &gitProviderImpl{workDir: workDir}
}

type gitProviderImpl struct {
	workDir    string
	gitUseCase domain.GitUseCase
}

var _ GitProvider = (*gitProviderImpl)(nil)

func (p *gitProviderImpl) GitUseCase() domain.GitUseCase {
	if p.gitUseCase == nil {
		p.gitUseCase = domain.NewGitUseCase(p.workDir, domaingit.NewGitRepository(p.workDir))
	}
	return p.gitUseCase
}
