package domain

import "context"

type ChildCounterOpts struct {
	UseWispsTable bool
}

type ChildCounterSQLRepository interface {
	NextChildID(ctx context.Context, parentID string, opts ChildCounterOpts) (string, error)
}
