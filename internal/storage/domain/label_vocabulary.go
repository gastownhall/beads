package domain

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// LabelVocabularyRepository is the SQL surface backing the opt-in label
// vocabulary registry (bd label define / undefine / defined). It has no
// relation to LabelSQLRepository, which reads and writes the labels actually
// stored on an issue: this repository reads and writes the SEPARATE registry
// of names a workspace has declared as its curated vocabulary.
type LabelVocabularyRepository interface {
	Define(ctx context.Context, label, description, actor string) error
	Undefine(ctx context.Context, label string) error
	List(ctx context.Context) ([]types.LabelDefinition, error)
}

// LabelVocabularyUseCase is the use-case surface the proxied-server route
// takes through uow.UnitOfWork.LabelVocabularyUseCase(). It has the same
// three methods as the repository below it -- there is no business logic to
// add on top of what issueops.DefineLabelInTx/UndefineLabelInTx/
// ListLabelDefinitionsInTx already enforce -- kept as its own thin type
// rather than a repository type alias so the direct-store and proxied routes
// depend on their own interface, matching every sibling use case in this
// package (ConfigUseCase, LabelUseCase, RawSQLUseCase).
type LabelVocabularyUseCase interface {
	Define(ctx context.Context, label, description, actor string) error
	Undefine(ctx context.Context, label string) error
	List(ctx context.Context) ([]types.LabelDefinition, error)
}

func NewLabelVocabularyUseCase(repo LabelVocabularyRepository) LabelVocabularyUseCase {
	return &labelVocabularyUseCaseImpl{repo: repo}
}

type labelVocabularyUseCaseImpl struct {
	repo LabelVocabularyRepository
}

var _ LabelVocabularyUseCase = (*labelVocabularyUseCaseImpl)(nil)

func (u *labelVocabularyUseCaseImpl) Define(ctx context.Context, label, description, actor string) error {
	return u.repo.Define(ctx, label, description, actor)
}

func (u *labelVocabularyUseCaseImpl) Undefine(ctx context.Context, label string) error {
	return u.repo.Undefine(ctx, label)
}

func (u *labelVocabularyUseCaseImpl) List(ctx context.Context) ([]types.LabelDefinition, error) {
	return u.repo.List(ctx)
}
