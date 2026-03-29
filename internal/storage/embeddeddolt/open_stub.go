//go:build !cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
)

// OpenSQL is a stub that returns an error when CGO is not enabled.
func OpenSQL(_ context.Context, _, _, _ string) (*sql.DB, func() error, error) {
	return nil, nil, errors.New("embeddeddolt: requires CGO (build with CGO_ENABLED=1)")
}
