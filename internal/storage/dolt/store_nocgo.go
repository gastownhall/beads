//go:build !cgo

package dolt

import (
	"context"
	"fmt"
)

var errNoCGO = fmt.Errorf("dolt: this binary was built without CGO support; rebuild with CGO_ENABLED=1")

// newEmbeddedMode returns an error in non-CGO builds.
// Embedded mode requires CGO for the dolthub/driver package.
// Use server mode (--server) to connect to an external dolt sql-server without CGO.
func newEmbeddedMode(_ context.Context, _ *Config) (*DoltStore, error) {
	return nil, fmt.Errorf("embedded mode requires CGO: %w\n\nTo use Dolt without CGO, connect to a dolt sql-server:\n  bd init --server --server-port 3307", errNoCGO)
}
