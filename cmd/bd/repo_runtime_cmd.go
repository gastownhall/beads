package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// commandRepoRuntime is the command-layer view of the active repo runtime.
// It keeps the resolved runtime and the target metadata config together so
// callers stop rediscovering host/port/database state independently.
type commandRepoRuntime struct {
	SourceBeadsDir string
	Runtime        *beads.RepoRuntime
	Config         *configfile.Config
}

func currentSourceBeadsDir() string {
	if beadsDir := strings.TrimSpace(os.Getenv("BEADS_DIR")); beadsDir != "" {
		return beadsDir
	}

	if redirect := beads.GetRedirectInfo(); redirect.IsRedirected && redirect.LocalDir != "" {
		return redirect.LocalDir
	}

	return beads.FindBeadsDir()
}

func loadCurrentRepoRuntime() (*commandRepoRuntime, error) {
	sourceBeadsDir := currentSourceBeadsDir()
	if sourceBeadsDir == "" {
		return nil, fmt.Errorf("not in a beads repository (no .beads directory found)")
	}

	runtime, err := beads.ResolveRepoRuntimeFromBeadsDir(sourceBeadsDir)
	if err != nil {
		return nil, err
	}

	cfg, err := configfile.Load(runtime.BeadsDir)
	if err != nil {
		cfg = configfile.DefaultConfig()
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	return &commandRepoRuntime{
		SourceBeadsDir: sourceBeadsDir,
		Runtime:        runtime,
		Config:         cfg,
	}, nil
}
