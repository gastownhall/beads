package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
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

// AddRemoteWithRef passes a non-empty ref as DOLT_REMOTE's --ref, which Dolt
// records as the remote's git_ref parameter and accepts for git-backed
// remotes only.
func (r *remoteSQLRepositoryImpl) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	if ref = strings.TrimSpace(ref); ref == "" {
		return r.AddRemote(ctx, name, url)
	}
	if err := r.vc.Remote(ctx, "add", "--ref", ref, name, url); err != nil {
		return fmt.Errorf("db: AddRemoteWithRef %s: %w", name, err)
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
	rows, err := r.runner.QueryContext(ctx, "SELECT name, url, params FROM dolt_remotes")
	if err != nil {
		return nil, fmt.Errorf("db: ListRemotes: query: %w", err)
	}
	defer rows.Close()

	var remotes []domain.Remote
	for rows.Next() {
		var rem domain.Remote
		var params sql.NullString
		if err := rows.Scan(&rem.Name, &rem.URL, &params); err != nil {
			return nil, fmt.Errorf("db: ListRemotes: scan: %w", err)
		}
		if params.Valid {
			ref, err := storage.GitRefFromParamsJSON(params.String)
			if err != nil {
				return nil, fmt.Errorf("db: ListRemotes: remote %s: %w", rem.Name, err)
			}
			rem.Ref = ref
		}
		remotes = append(remotes, rem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: ListRemotes: rows: %w", err)
	}
	return remotes, nil
}
