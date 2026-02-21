package doctor

import (
	"database/sql"
	"os"
)

func openDeepValidationDB(beadsDir string, sqliteDBPath string) (*sql.DB, func(), error) {
	if info, err := os.Stat(sqliteDBPath); err == nil && info.IsDir() {
		conn, err := openDoltDBWithLock(beadsDir)
		if err != nil {
			return nil, func() {}, err
		}
		return conn.db, conn.Close, nil
	}

	db, err := sql.Open("sqlite3", sqliteConnString(sqliteDBPath, true))
	if err != nil {
		return nil, func() {}, err
	}

	return db, func() { _ = db.Close() }, nil
}
