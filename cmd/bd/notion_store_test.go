//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestGetNotionConfigReadsDBPathWhenStoreUnset(t *testing.T) {
	saveAndRestoreGlobals(t)
	tempDir := t.TempDir()
	testDBPath := filepath.Join(tempDir, "test.db")
	testStore := newTestStore(t, testDBPath)
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "notion.token", "path-token"); err != nil {
		t.Fatalf("SetConfig(notion.token): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.data_source_id", "path-ds"); err != nil {
		t.Fatalf("SetConfig(notion.data_source_id): %v", err)
	}

	store = nil
	dbPath = testDBPath
	t.Setenv("NOTION_TOKEN", "")
	t.Setenv("NOTION_DATA_SOURCE_ID", "")
	t.Setenv("NOTION_VIEW_URL", "")

	cfg := getNotionConfig()
	if cfg.DataSourceID != "path-ds" {
		t.Fatalf("config = %+v", cfg)
	}
}
