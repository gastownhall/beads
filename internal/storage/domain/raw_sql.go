package domain

import "context"

type RawSQLResult struct {
	Columns []string
	Rows    [][]any
}

type RawSQLRepository interface {
	Query(ctx context.Context, query string, args ...any) (*RawSQLResult, error)
	Exec(ctx context.Context, query string, args ...any) (int64, error)
}

type RawSQLUseCase interface {
	Query(ctx context.Context, query string, args ...any) (*RawSQLResult, error)
	Exec(ctx context.Context, query string, args ...any) (int64, error)
}

func NewRawSQLUseCase(repo RawSQLRepository) RawSQLUseCase {
	return &rawSQLUseCaseImpl{repo: repo}
}

type rawSQLUseCaseImpl struct {
	repo RawSQLRepository
}

var _ RawSQLUseCase = (*rawSQLUseCaseImpl)(nil)

func (u *rawSQLUseCaseImpl) Query(ctx context.Context, query string, args ...any) (*RawSQLResult, error) {
	return u.repo.Query(ctx, query, args...)
}

func (u *rawSQLUseCaseImpl) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return u.repo.Exec(ctx, query, args...)
}
