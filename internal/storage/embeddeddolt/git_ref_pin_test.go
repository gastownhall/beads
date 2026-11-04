//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/dolthub/dolt/go/libraries/doltcore/dbfactory"
	"github.com/dolthub/dolt/go/store/blobstore"

	"github.com/steveyegge/beads/internal/storage"
)

// The storage package pins two Dolt literals so that the binary does not
// import Dolt outside the storage backends. A Dolt bump that renamed either
// would make every ref remote read as the default and route to
// refs/dolt/data silently; this keeps the pins honest against the module
// the engine is built from.
func TestGitRefPinsMatchDolt(t *testing.T) {
	if storage.GitRefParam != dbfactory.GitRefParam {
		t.Errorf("storage.GitRefParam = %q, dolt dbfactory.GitRefParam = %q", storage.GitRefParam, dbfactory.GitRefParam)
	}
	if storage.DefaultGitDataRef != blobstore.DoltDataRef {
		t.Errorf("storage.DefaultGitDataRef = %q, dolt blobstore.DoltDataRef = %q", storage.DefaultGitDataRef, blobstore.DoltDataRef)
	}
}
