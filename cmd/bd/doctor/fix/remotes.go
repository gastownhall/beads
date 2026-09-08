package fix

import (
	"database/sql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func openFixDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	host := cfg.GetDoltServerHost()
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()
	password := cfg.GetDoltServerPassword()
	port := doltserver.DefaultConfig(beadsDir).Port

	connStr := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Database: database,
		TLS:      cfg.GetDoltServerTLS(),
	}.String()
	return sql.Open("mysql", connStr)
}
