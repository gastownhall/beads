//go:build !embeddeddolt

package embeddeddolt

import (
	"context"
	"errors"
)

// EmbeddedDoltStore is a stub for builds without the embeddeddolt tag.
type EmbeddedDoltStore struct{}

// New returns an error when the embeddeddolt build tag is not set.
func New(_ context.Context, _, _, _ string) (*EmbeddedDoltStore, error) {
	return nil, errors.New("embeddeddolt: wip, do not use")
}
