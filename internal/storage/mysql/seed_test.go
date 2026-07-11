package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestSeedOnlyOnFirstProvision guards the same regression the Postgres backend has:
// InitSchema runs on every open, so seeding default config unconditionally would
// resurrect a key the user `config unset`. Seeding must be gated on first provision.
func TestSeedOnlyOnFirstProvision(t *testing.T) {
	url := os.Getenv("BEADS_MYSQL_TEST_URL")
	if url == "" {
		t.Skip("BEADS_MYSQL_TEST_URL not set")
	}
	database := fmt.Sprintf("seedonce_%d", time.Now().UnixNano())
	ctx := context.Background()
	t.Cleanup(func() {
		if serverDSN, e := withDatabase(url, ""); e == nil {
			if srv, e2 := sql.Open("mysql", serverDSN); e2 == nil {
				_, _ = srv.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+database+"`")
				_ = srv.Close()
			}
		}
	})

	st, err := Provision(ctx, url, database)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if v, _ := st.GetConfig(ctx, "auto_compact_enabled"); v != "false" {
		t.Fatalf("first provision: want seeded auto_compact_enabled=false, got %q", v)
	}
	if err := st.DeleteConfig(ctx, "auto_compact_enabled"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if v, _ := st.GetConfig(ctx, "auto_compact_enabled"); v != "" {
		t.Fatalf("after unset: want empty, got %q", v)
	}
	_ = st.Close()

	st2, err := Provision(ctx, url, database)
	if err != nil {
		t.Fatalf("re-Provision: %v", err)
	}
	defer st2.Close()
	if v, _ := st2.GetConfig(ctx, "auto_compact_enabled"); v != "" {
		t.Fatalf("REGRESSION: re-provision resurrected an unset key: auto_compact_enabled=%q (want empty)", v)
	}
}
