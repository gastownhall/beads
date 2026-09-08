package db

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func NewEventsJournalSQLRepository(runner Runner) domain.EventsJournalSQLRepository {
	return &eventsJournalSQLRepository{runner: runner}
}

type eventsJournalSQLRepository struct {
	runner Runner
}

var _ domain.EventsJournalSQLRepository = (*eventsJournalSQLRepository)(nil)

func (r *eventsJournalSQLRepository) Read(ctx context.Context, since int64, limit int) ([]storage.EventsJournalRow, error) {
	return issueops.ReadEventsInTx(ctx, r.runner, since, limit)
}

func (r *eventsJournalSQLRepository) ReadPage(ctx context.Context, since int64, limit int) (storage.EventsJournalPage, error) {
	return issueops.ReadEventsPageInTx(ctx, r.runner, since, limit)
}

func (r *eventsJournalSQLRepository) Prune(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
	return issueops.PruneEventsInTx(ctx, r.runner, before, retainDays, retainRows, time.Now().UTC())
}
