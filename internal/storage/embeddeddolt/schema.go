//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage/schema"
)

// LatestVersion delegates to the shared schema package.
func LatestVersion() int {
	return schema.LatestVersion()
}
