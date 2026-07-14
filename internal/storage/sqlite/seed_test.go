package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSeedOnlyOnFirstProvision guards that re-opening a SQLite workspace (InitSchema
// runs every open) does not resurrect a key the user `config unset`.
func TestSeedOnlyOnFirstProvision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seed.db")

	st, err := Provision(ctx, path)
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

	st2, err := Provision(ctx, path)
	if err != nil {
		t.Fatalf("re-Provision: %v", err)
	}
	defer st2.Close()
	if v, _ := st2.GetConfig(ctx, "auto_compact_enabled"); v != "" {
		t.Fatalf("REGRESSION: re-provision resurrected an unset key: got %q (want empty)", v)
	}
}
