package proxy

import "context"

type DatabaseProxy interface {
	ListenAndServe(ctx context.Context) error
}
