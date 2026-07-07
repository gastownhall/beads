package fs

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	domainfs "github.com/steveyegge/beads/internal/storage/domain/fs"
)

type FileSystemProvider interface {
	BeadsDirFSUseCase() domain.BeadsDirFSUseCase
}

func NewFileSystemProvider(workDir string, templates domain.BeadsDirTemplates, adapters domain.BeadsDirFSAdapters) FileSystemProvider {
	return &fileSystemProviderImpl{
		workDir:   workDir,
		templates: templates,
		adapters:  adapters,
	}
}

type fileSystemProviderImpl struct {
	workDir           string
	templates         domain.BeadsDirTemplates
	adapters          domain.BeadsDirFSAdapters
	beadsDirFSUseCase domain.BeadsDirFSUseCase
}

var _ FileSystemProvider = (*fileSystemProviderImpl)(nil)

func (p *fileSystemProviderImpl) BeadsDirFSUseCase() domain.BeadsDirFSUseCase {
	if p.beadsDirFSUseCase == nil {
		p.beadsDirFSUseCase = domain.NewBeadsDirFSUseCase(
			domainfs.NewBeadsDirFSRepository(p.workDir, p.templates),
			p.adapters,
		)
	}
	return p.beadsDirFSUseCase
}
