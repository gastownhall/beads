package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/utils"
)

// initDBRef names one database and where it lives.
type initDBRef struct {
	Database string // raw recorded or flagged name; "" = unset
	Server   bool
}

// databaseOrDefault is the database a store open of this ref selects.
func (r initDBRef) databaseOrDefault() string {
	if r.Database == "" {
		return configfile.DefaultDoltDatabase
	}
	return r.Database
}

// initIdentity is what bd init knows about the database it is about to
// replace (Configured: the one the workspace's commands open) and the one it
// is about to write (Target) (#6172). Init's guards reason about Configured;
// the metadata write records Target. Before this existed the guards read the
// env-first getters, so an exported BEADS_DOLT_SERVER_DATABASE could point the
// destroy count at a database that does not exist, and a failed count skips
// the confirmation.
type initIdentity struct {
	BeadsDir       string             // the .beads init writes (follows .beads/redirect)
	RedirectedFrom string             // the stub .beads when a redirect was followed
	Recorded       *configfile.Config // metadata.json at BeadsDir; nil = fresh
	Configured     initDBRef
	Target         initDBRef
}

// initIdentityFlags are the invocation's own database selectors.
type initIdentityFlags struct {
	Database string // --database; "" when not given
}

// resolveInitWorkspace resolves the .beads init writes and reads what it
// records. It runs once, before any init flag is promoted into the
// environment.
func resolveInitWorkspace() initIdentity {
	var id initIdentity
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		id.BeadsDir = utils.CanonicalizePath(envBeadsDir)
	} else if fallback := beads.GetWorktreeFallbackBeadsDir(); fallback != "" {
		id.BeadsDir = fallback
	} else {
		localBeadsDir := filepath.Join(".", ".beads")
		id.BeadsDir = beads.FollowRedirect(localBeadsDir)
		if id.BeadsDir != localBeadsDir {
			id.RedirectedFrom = localBeadsDir
		}
	}
	if cfg, err := configfile.LoadForDiscovery(id.BeadsDir); err == nil {
		id.Recorded = cfg
	}
	return id
}

// resolveInitIdentity derives Configured from what the workspace records and
// Target from Configured overlaid by the invocation's flags. It is pure.
func resolveInitIdentity(ws initIdentity, flags initIdentityFlags, serverMode bool) initIdentity {
	id := ws
	if id.Recorded != nil {
		id.Configured = initDBRef{
			Database: id.Recorded.DoltDatabase,
			Server:   id.Recorded.IsDoltServerMode(),
		}
	}
	id.Target = id.Configured
	id.Target.Server = serverMode
	if flags.Database != "" {
		id.Target.Database = flags.Database
	}
	return id
}

// configuredRef is the database the guards check for a guard run at the
// init target. Through a redirect the guards still read the stub .beads, so
// they get no override there.
func (id initIdentity) configuredRef() *initDBRef {
	if id.Recorded == nil || id.RedirectedFrom != "" {
		return nil
	}
	ref := id.Configured
	return &ref
}

// countConfiguredIssues counts the issues in the configured database, the one
// the workspace's commands open, never the one an exported variable or an
// init flag names. Returns 0 for a fresh workspace.
func countConfiguredIssues(id initIdentity) (int, error) {
	if id.Recorded == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := openConfiguredStoreForCount(ctx, id)
	if err != nil {
		return 0, err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, nil
	}
	return stats.TotalIssues, nil
}

// openConfiguredStoreForCount opens the configured database read-only. A
// server-mode workspace pins the recorded database name, so the open neither
// follows BEADS_DOLT_SERVER_DATABASE nor creates anything. Every other mode
// opens the workspace as its commands do.
func openConfiguredStoreForCount(ctx context.Context, id initIdentity) (storage.DoltStorage, error) {
	if id.Recorded.GetBackend() == configfile.BackendDolt && id.Configured.Server {
		return dolt.NewFromConfigWithOptions(ctx, id.BeadsDir, &dolt.Config{
			Database: id.Configured.databaseOrDefault(),
			ReadOnly: true,
		})
	}
	return newDoltStoreFromConfig(ctx, id.BeadsDir)
}
