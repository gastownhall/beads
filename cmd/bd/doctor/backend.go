package doctor

import (
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
)

// getBackendAndBeadsDir resolves the effective .beads directory (following redirects)
// and returns the configured storage backend ("dolt" by default).
func getBackendAndBeadsDir(repoPath string) (backend string, beadsDir string) {
	beadsDir = resolveBeadsDir(filepath.Join(repoPath, ".beads"))

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return configfile.BackendDolt, beadsDir
	}
	return cfg.GetBackend(), beadsDir
}
