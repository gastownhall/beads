package db

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
)

func NewRemoteSQLRepository(runner Runner) domain.RemoteSQLRepository {
	return &remoteSQLRepositoryImpl{
		runner: runner,
		vc:     NewDoltVersionControlSQLRepository(runner),
	}
}

type remoteSQLRepositoryImpl struct {
	runner Runner
	vc     DoltVersionControlSQLRepository
}

var _ domain.RemoteSQLRepository = (*remoteSQLRepositoryImpl)(nil)

func (r *remoteSQLRepositoryImpl) AddRemote(ctx context.Context, name, url string) error {
	if err := r.vc.Remote(ctx, "add", name, url); err != nil {
		return fmt.Errorf("db: AddRemote %s: %w", name, err)
	}
	return nil
}

func (r *remoteSQLRepositoryImpl) RemoveRemote(ctx context.Context, name string) error {
	if err := r.vc.Remote(ctx, "remove", name); err != nil {
		return fmt.Errorf("db: RemoveRemote %s: %w", name, err)
	}
	return nil
}

func (r *remoteSQLRepositoryImpl) ListRemotes(ctx context.Context) ([]domain.Remote, error) {
	rows, err := r.runner.QueryContext(ctx, "SELECT name, url FROM dolt_remotes")
	if err != nil {
		return nil, fmt.Errorf("db: ListRemotes: query: %w", err)
	}
	defer rows.Close()

	var remotes []domain.Remote
	for rows.Next() {
		var rem domain.Remote
		if err := rows.Scan(&rem.Name, &rem.URL); err != nil {
			return nil, fmt.Errorf("db: ListRemotes: scan: %w", err)
		}
		remotes = append(remotes, rem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: ListRemotes: rows: %w", err)
	}
	return remotes, nil
}
