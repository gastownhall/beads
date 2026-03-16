package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openReadOnlyStoreForDBPath reopens a read-only store from an existing dbPath
// while preserving repo-local metadata such as dolt_database and the resolved
// Dolt server port. Falls back to a raw path-only open when no matching
// metadata.json can be found.
func openReadOnlyStoreForDBPath(ctx context.Context, dbPath string) (*dolt.DoltStore, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("no database path available")
	}

	if runtime, err := beads.ResolveRepoRuntimeFromDBPath(dbPath); err == nil && runtime != nil {
		cfg := &dolt.Config{
			Path:           runtime.DatabasePath,
			ReadOnly:       true,
			BeadsDir:       runtime.BeadsDir,
			Database:       runtime.Database,
			ServerHost:     runtime.Host,
			ServerPort:     runtime.Port,
			ServerUser:     runtime.User,
			ServerPassword: os.Getenv("BEADS_DOLT_PASSWORD"),
			ServerTLS:      runtime.TLS,
		}
		if !runtime.ExplicitPort {
			dolt.ApplyCLIAutoStart(runtime.BeadsDir, cfg)
		}
		return dolt.New(ctx, cfg)
	}

	return dolt.New(ctx, &dolt.Config{Path: dbPath, ReadOnly: true})
}

// resolveBeadsDirForDBPath maps a database path back to its owning .beads
// directory when metadata.json is available. This is needed for repos that use
// non-default dolt_database names or custom dolt_data_dir locations.
func resolveBeadsDirForDBPath(dbPath string) string {
	runtime, err := beads.ResolveRepoRuntimeFromDBPath(dbPath)
	if err == nil && runtime != nil {
		return runtime.BeadsDir
	}

	return ""
}
