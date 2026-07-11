package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// TestSeedOnlyOnFirstProvision is the config_unset_seeded_roundtrip regression: the
// bts-rs differential oracle caught that seeding default config on EVERY open (InitSchema
// runs per NewFromConfig/Provision) resurrected any key a user had `config unset`. Seeding
// must happen only on the first provision, like Dolt's one-shot migration 0016.
func TestSeedOnlyOnFirstProvision(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres seed test")
	}
	schema := fmt.Sprintf("seedonce_%d", time.Now().UnixNano())
	ctx := context.Background()
	t.Cleanup(func() {
		if raw, err := pgdialect.OpenRaw(url, "public"); err == nil {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
			_ = raw.Close()
		}
	})

	st, err := Provision(ctx, url, schema)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// First provision seeds the default config rows.
	if v, _ := st.GetConfig(ctx, "auto_compact_enabled"); v != "false" {
		t.Fatalf("first provision: want seeded auto_compact_enabled=false, got %q", v)
	}
	// User unsets a seeded key.
	if err := st.DeleteConfig(ctx, "auto_compact_enabled"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if v, _ := st.GetConfig(ctx, "auto_compact_enabled"); v != "" {
		t.Fatalf("after unset: want empty, got %q", v)
	}
	_ = st.Close()

	// Re-open the SAME schema — InitSchema runs again. The unset key must STAY unset.
	st2, err := Provision(ctx, url, schema)
	if err != nil {
		t.Fatalf("re-Provision: %v", err)
	}
	defer st2.Close()
	if v, _ := st2.GetConfig(ctx, "auto_compact_enabled"); v != "" {
		t.Fatalf("REGRESSION: re-provision resurrected an unset key: auto_compact_enabled=%q (want empty)", v)
	}
}
