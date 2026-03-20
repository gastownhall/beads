package notion

import (
	"context"
	"testing"
)

type configStore struct {
	data map[string]string
}

func (s *configStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.data[key], nil
}

func TestResolveRuntimeConfigUsesStoredValuesWithoutExplicitOverrides(t *testing.T) {
	t.Parallel()

	cfg := ResolveRuntimeConfig(context.Background(), &configStore{
		data: map[string]string{
			"notion.ncli_bin":    "/store/ncli",
			"notion.database_id": "store-db",
			"notion.view_url":    "https://store/view",
		},
	}, RuntimeConfigInput{})

	if cfg.BinaryPath != "/store/ncli" {
		t.Fatalf("BinaryPath = %q, want /store/ncli", cfg.BinaryPath)
	}
	if cfg.DatabaseID != "store-db" {
		t.Fatalf("DatabaseID = %q, want store-db", cfg.DatabaseID)
	}
	if cfg.ViewURL != "https://store/view" {
		t.Fatalf("ViewURL = %q, want https://store/view", cfg.ViewURL)
	}
}

func TestResolveRuntimeConfigPrefersExplicitOverrides(t *testing.T) {
	t.Parallel()

	cfg := ResolveRuntimeConfig(context.Background(), &configStore{
		data: map[string]string{
			"notion.ncli_bin":    "/store/ncli",
			"notion.database_id": "store-db",
			"notion.view_url":    "https://store/view",
		},
	}, RuntimeConfigInput{
		BinaryPath:    "/flag/ncli",
		BinaryPathSet: true,
		DatabaseID:    "flag-db",
		DatabaseIDSet: true,
		ViewURL:       "https://flag/view",
		ViewURLSet:    true,
	})

	if cfg.BinaryPath != "/flag/ncli" {
		t.Fatalf("BinaryPath = %q, want /flag/ncli", cfg.BinaryPath)
	}
	if cfg.DatabaseID != "flag-db" {
		t.Fatalf("DatabaseID = %q, want flag-db", cfg.DatabaseID)
	}
	if cfg.ViewURL != "https://flag/view" {
		t.Fatalf("ViewURL = %q, want https://flag/view", cfg.ViewURL)
	}
}
