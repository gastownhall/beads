package db

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// NewLabelVocabularySQLRepository builds the proxied-server (uow) SQL
// repository for the label vocabulary registry. It delegates every method to
// the same issueops.*InTx functions the direct-store route calls: Runner's
// method set is identical to issueops.DBTX's, so the two write stacks share
// one SQL implementation instead of maintaining a second copy of it (unlike
// LabelSQLRepository/AddLabelInTx, which still carry two independent copies
// from before this pattern was adopted -- see label.go's history).
func NewLabelVocabularySQLRepository(runner Runner) domain.LabelVocabularyRepository {
	return &labelVocabularySQLRepositoryImpl{runner: runner}
}

type labelVocabularySQLRepositoryImpl struct {
	runner Runner
}

var _ domain.LabelVocabularyRepository = (*labelVocabularySQLRepositoryImpl)(nil)

func (r *labelVocabularySQLRepositoryImpl) Define(ctx context.Context, label, description, actor string) error {
	return issueops.DefineLabelInTx(ctx, r.runner, label, description, actor)
}

func (r *labelVocabularySQLRepositoryImpl) Undefine(ctx context.Context, label string) error {
	return issueops.UndefineLabelInTx(ctx, r.runner, label)
}

func (r *labelVocabularySQLRepositoryImpl) List(ctx context.Context) ([]types.LabelDefinition, error) {
	return issueops.ListLabelDefinitionsInTx(ctx, r.runner)
}
