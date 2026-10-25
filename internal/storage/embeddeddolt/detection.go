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
	return len(Databases(beadsDir)) > 0
}

// Databases names, sorted, the databases of beadsDir's embedded Dolt
// repository: the non-symlink directories under embeddeddolt that pass
// HasRepository's .dolt marker probe. It is empty when beadsDir holds none.
func Databases(beadsDir string) []string {
	root := filepath.Join(beadsDir, "embeddeddolt")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var databases []string
	for _, database := range entries {
		if !database.IsDir() || database.Type()&os.ModeSymlink != 0 {
			continue
		}
		marker, err := os.Lstat(filepath.Join(root, database.Name(), ".dolt"))
		if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
			continue
		}
		markerEntries, err := os.ReadDir(filepath.Join(root, database.Name(), ".dolt"))
		if err == nil && len(markerEntries) > 0 {
			databases = append(databases, database.Name())
		}
	}
	return databases
}
