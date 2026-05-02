//go:build !cgo

package embeddeddolt

import (
	"context"
	"errors"
)

// EmbeddedDoltStore is a stub for builds without CGO.
type EmbeddedDoltStore struct {
	dataDir  string
	database string
	branch   string
}

var errNoCGO = errors.New("embeddeddolt: requires CGO (build with CGO_ENABLED=1)")

// Open returns an error when CGO is not enabled.
func Open(_ context.Context, _, _, _ string) (*EmbeddedDoltStore, error) {
	return nil, errNoCGO
}
