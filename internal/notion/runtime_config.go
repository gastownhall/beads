package notion

import (
	"context"
	"os"
	"strings"
)

type configReader interface {
	GetConfig(ctx context.Context, key string) (string, error)
}

// RuntimeConfig captures the effective runtime settings for Notion operations.
type RuntimeConfig struct {
	BinaryPath string
	DatabaseID string
	ViewURL    string
}

// RuntimeConfigInput describes explicit overrides before store/env resolution.
type RuntimeConfigInput struct {
	BinaryPath    string
	BinaryPathSet bool
	DatabaseID    string
	DatabaseIDSet bool
	ViewURL       string
	ViewURLSet    bool
}

// ResolveRuntimeConfig applies the shared precedence for Notion runtime settings.
//
// Precedence is:
//  1. Explicit CLI/tracker override
//  2. Repo-scoped beads config
//  3. Environment variable
//  4. Built-in default (binary only)
func ResolveRuntimeConfig(ctx context.Context, store configReader, input RuntimeConfigInput) RuntimeConfig {
	if ctx == nil {
		ctx = context.Background()
	}

	return RuntimeConfig{
		BinaryPath: resolveRuntimeValue(ctx, store, input.BinaryPath, input.BinaryPathSet, "notion.ncli_bin", "NCLI_BIN", DefaultBinaryPath),
		DatabaseID: resolveRuntimeValue(ctx, store, input.DatabaseID, input.DatabaseIDSet, "notion.database_id", "NOTION_DATABASE_ID", ""),
		ViewURL:    resolveRuntimeValue(ctx, store, input.ViewURL, input.ViewURLSet, "notion.view_url", "NOTION_VIEW_URL", ""),
	}
}

func resolveRuntimeValue(ctx context.Context, store configReader, explicit string, explicitSet bool, configKey, envKey, fallback string) string {
	if explicitSet {
		return strings.TrimSpace(explicit)
	}
	if store != nil {
		if value, err := store.GetConfig(ctx, configKey); err == nil {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	if envValue := strings.TrimSpace(os.Getenv(envKey)); envValue != "" {
		return envValue
	}
	return fallback
}
