//go:build !embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
)

// OpenSQL is a stub that returns an error when the embeddeddolt build tag is
// not set. Build with -tags embeddeddolt to enable.
func OpenSQL(_ context.Context, _, _, _ string) (*sql.DB, func() error, error) {
	return nil, nil, errors.New("embeddeddolt: build with -tags embeddeddolt to enable")
}
