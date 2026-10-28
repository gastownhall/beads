//go:build !cgo

package main

import (
	"context"
	"errors"
)

// countEmbeddedInitIssues cannot read an embedded database without CGO.
func countEmbeddedInitIssues(_ context.Context, _, _ string) (int, error) {
	return 0, errors.New("embedded Dolt requires CGO (build with CGO_ENABLED=1)")
}
