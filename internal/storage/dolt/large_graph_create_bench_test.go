package dolt

import (
	"database/sql"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/batchbench"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// TestLargeGraphCreateTiming_Dolt runs the large-graph create benchmark
// (internal/storage/batchbench) on the Dolt server backend. Opt-in via
// batchbench.EnvVar; it logs and asserts nothing.
func TestLargeGraphCreateTiming_Dolt(t *testing.T) {
	issues := batchbench.Issues(t)
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatal(err)
	}
	// Populating the fixture and the per-edge reads of the code being
	// compared can outlast the store's 10s read timeout.
	cfg.ReadTimeout, cfg.WriteTimeout = 30*time.Minute, 30*time.Minute
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	counts := &sqlcount.Counts{}
	db := sql.OpenDB(sqlcount.WrapConnector(connector, counts))
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	batchbench.Run(t, db, counts, "test", issues)
}
