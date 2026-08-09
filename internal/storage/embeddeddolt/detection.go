package embeddeddolt

import (
	"os"
	"path/filepath"
)

// HasRepository reports whether beadsDir contains an embedded Dolt repository.
// It owns the adapter's coarse .dolt marker probe: the marker must be a
// non-symlink directory with at least one entry, but entry names and private
// repository files are never interpreted or opened.
func HasRepository(beadsDir string) bool {
	root := filepath.Join(beadsDir, "embeddeddolt")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	databases, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, database := range databases {
		if !database.IsDir() || database.Type()&os.ModeSymlink != 0 {
			continue
		}
		marker, err := os.Lstat(filepath.Join(root, database.Name(), ".dolt"))
		if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, database.Name(), ".dolt"))
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}
