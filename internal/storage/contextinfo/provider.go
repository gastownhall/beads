package contextinfo

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	domainfs "github.com/steveyegge/beads/internal/storage/domain/fs"
)

type ContextProvider interface {
	ContextUseCase() domain.ContextUseCase
}

func NewContextProvider(workDir, version string) ContextProvider {
	return &contextProviderImpl{workDir: workDir, version: version}
}

type contextProviderImpl struct {
	workDir        string
	version        string
	contextUseCase domain.ContextUseCase
}

var _ ContextProvider = (*contextProviderImpl)(nil)

func (p *contextProviderImpl) ContextUseCase() domain.ContextUseCase {
	if p.contextUseCase == nil {
		p.contextUseCase = domain.NewContextUseCase(
			domainfs.NewContextRepository(domainfs.NewBeadsDirFSRepository(p.workDir, domain.BeadsDirTemplates{})),
			p.version,
		)
	}
	return p.contextUseCase
}
