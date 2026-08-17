//go:build cgo

package embeddeddolt

import (
	"github.com/jonbaldie/beads/internal/storage/schema"
)

func LatestVersion() int {
	return schema.LatestVersion()
}

func LatestIgnoredVersion() int {
	return schema.LatestIgnoredVersion()
}
