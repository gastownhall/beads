//go:build !cgo

package legacysqlite

import (
	"context"
	"fmt"
	"io"
)

// Export keeps the command surface available in no-CGO builds.
func Export(context.Context, string, string, io.Writer) error {
	return fmt.Errorf("legacy SQLite migration requires CGO")
}
