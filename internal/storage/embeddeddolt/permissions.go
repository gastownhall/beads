//go:build cgo

package embeddeddolt

import (
	"fmt"
	"os"
	"sync"

	"github.com/dolthub/dolt/go/store/util/tempfiles"

	"github.com/steveyegge/beads/internal/config"
)

var configureDoltTempFilePermissionsOnce sync.Once

// configureDoltTempFilePermissions makes Dolt's rename-based writes honor the
// same owner-and-group access contract as the rest of .beads. Dolt creates its
// manifest through os.CreateTemp, whose fixed 0600 mode otherwise survives the
// rename even when the containing directories and umask allow group access.
func configureDoltTempFilePermissions() {
	configureDoltTempFilePermissionsOnce.Do(func() {
		tempfiles.MovableTempFileProvider = groupWritableTempFileProvider{
			TempFileProvider: tempfiles.MovableTempFileProvider,
		}
	})
}

type groupWritableTempFileProvider struct {
	tempfiles.TempFileProvider
}

func (p groupWritableTempFileProvider) NewFile(dir, pattern string) (*os.File, error) {
	f, err := p.TempFileProvider.NewFile(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(config.BeadsFilePerm); err != nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("set Dolt temporary file permissions: %w", err)
	}
	return f, nil
}
