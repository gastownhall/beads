package fix

import "github.com/steveyegge/beads/internal/storage"

func sqliteConnString(path string, readOnly bool) string {
	return storage.SQLiteConnString(path, readOnly)
}
