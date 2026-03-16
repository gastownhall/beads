package fix

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

type repoRuntimeInfo struct {
	Runtime      *beads.RepoRuntime
	Config       *configfile.Config
	SourceConfig *configfile.Config
}

func resolveRuntimeInfoForRepo(repoPath string) (*repoRuntimeInfo, error) {
	runtime, err := beads.ResolveRepoRuntimeFromRepoPath(repoPath)
	if err == nil && runtime != nil {
		cfg, cfgErr := configfile.Load(runtime.BeadsDir)
		if cfgErr != nil {
			return nil, fmt.Errorf("failed to load config: %w", cfgErr)
		}
		return &repoRuntimeInfo{
			Runtime:      runtime,
			Config:       effectiveFixConfig(cfg),
			SourceConfig: loadSourceConfig(runtime, cfg),
		}, nil
	}

	sourceBeadsDir := filepath.Join(repoPath, ".beads")
	beadsDir := resolveBeadsDir(sourceBeadsDir)
	cfg, cfgErr := configfile.Load(beadsDir)
	if cfgErr != nil {
		return nil, fmt.Errorf("failed to load config: %w", cfgErr)
	}
	cfgEffective := effectiveFixConfig(cfg)

	return &repoRuntimeInfo{
		Runtime: &beads.RepoRuntime{
			RepoPath:         repoPath,
			SourceBeadsDir:   sourceBeadsDir,
			BeadsDir:         beadsDir,
			Backend:          cfgEffective.GetBackend(),
			DatabasePath:     cfgEffective.DatabasePath(beadsDir),
			Database:         cfgEffective.GetDoltDatabase(),
			DoltDataDir:      cfgEffective.GetDoltDataDir(),
			DoltMode:         cfgEffective.GetDoltMode(),
			ServerMode:       cfgEffective.IsDoltServerMode(),
			Host:             cfgEffective.GetDoltServerHost(),
			Port:             doltserver.DefaultConfig(beadsDir).Port,
			User:             cfgEffective.GetDoltServerUser(),
			TLS:              cfgEffective.GetDoltServerTLS(),
			SharedServerMode: doltserver.IsSharedServerMode(),
		},
		Config:       cfgEffective,
		SourceConfig: cfg,
	}, nil
}

func effectiveFixConfig(cfg *configfile.Config) *configfile.Config {
	if cfg != nil {
		return cfg
	}
	return configfile.DefaultConfig()
}

func loadSourceConfig(runtime *beads.RepoRuntime, fallback *configfile.Config) *configfile.Config {
	if runtime == nil || runtime.SourceBeadsDir == "" || runtime.SourceBeadsDir == runtime.BeadsDir {
		return fallback
	}

	cfg, err := configfile.Load(runtime.SourceBeadsDir)
	if err == nil && cfg != nil {
		return cfg
	}
	return fallback
}

func metadataConfigForRepo(info *repoRuntimeInfo) (*configfile.Config, string) {
	if info == nil || info.Runtime == nil {
		return nil, ""
	}
	if info.SourceConfig != nil && info.Runtime.SourceBeadsDir != "" {
		return info.SourceConfig, info.Runtime.SourceBeadsDir
	}
	return info.Config, info.Runtime.BeadsDir
}

func runtimeDatabaseDir(runtime *beads.RepoRuntime) string {
	if runtime == nil || runtime.DatabasePath == "" || runtime.Database == "" {
		return ""
	}
	return filepath.Join(runtime.DatabasePath, runtime.Database)
}

func openFixDBForRuntime(runtime *beads.RepoRuntime, cfg *configfile.Config) (*sql.DB, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime required")
	}
	cfg = effectiveFixConfig(cfg)

	host := runtime.Host
	if host == "" {
		host = configfile.DefaultDoltServerHost
	}
	user := runtime.User
	if user == "" {
		user = configfile.DefaultDoltServerUser
	}
	database := runtime.Database
	if database == "" {
		database = cfg.GetDoltDatabase()
	}
	if database == "" {
		database = configfile.DefaultDoltDatabase
	}
	port := runtime.Port
	if port == 0 {
		port = doltserver.DefaultConfig(runtime.BeadsDir).Port
	}
	if port == 0 {
		return nil, fmt.Errorf("no Dolt server port configured and no server running")
	}

	password := cfg.GetDoltServerPassword()
	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, password, host, port, database)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, host, port, database)
	}
	return sql.Open("mysql", connStr)
}

func openVerifiedFixDBForRuntime(runtime *beads.RepoRuntime, cfg *configfile.Config) (*sql.DB, error) {
	db, err := openFixDBForRuntime(runtime, cfg)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dolt server not reachable: %w", err)
	}
	return db, nil
}

func openDoltDBForRepoPath(repoPath string) (*sql.DB, error) {
	info, err := resolveRuntimeInfoForRepo(repoPath)
	if err != nil {
		return nil, err
	}
	db, err := openVerifiedFixDBForRuntime(info.Runtime, info.Config)
	if err != nil {
		return nil, fmt.Errorf("dolt server connection failed: %w", err)
	}
	return db, nil
}

func newDoltStoreForRepoPath(ctx context.Context, repoPath string, createIfMissing bool) (*dolt.DoltStore, error) {
	info, err := resolveRuntimeInfoForRepo(repoPath)
	if err != nil {
		return nil, err
	}
	if info.Runtime == nil {
		return nil, fmt.Errorf("repo runtime unavailable")
	}

	cfg := &dolt.Config{
		Path:            info.Runtime.DatabasePath,
		BeadsDir:        info.Runtime.BeadsDir,
		Database:        info.Runtime.Database,
		ServerHost:      info.Runtime.Host,
		ServerPort:      info.Runtime.Port,
		ServerUser:      info.Runtime.User,
		ServerPassword:  info.Config.GetDoltServerPassword(),
		ServerTLS:       info.Runtime.TLS,
		CreateIfMissing: createIfMissing,
	}
	if cfg.ServerHost == "" {
		cfg.ServerHost = configfile.DefaultDoltServerHost
	}
	if cfg.ServerUser == "" {
		cfg.ServerUser = configfile.DefaultDoltServerUser
	}
	if cfg.Database == "" {
		cfg.Database = configfile.DefaultDoltDatabase
	}
	dolt.ApplyCLIAutoStart(info.Runtime.BeadsDir, cfg)

	store, err := dolt.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func openDoltStoreForRepoPath(ctx context.Context, repoPath string) (*dolt.DoltStore, error) {
	return newDoltStoreForRepoPath(ctx, repoPath, false)
}

func createDoltStoreForRepoPath(ctx context.Context, repoPath string) (*dolt.DoltStore, error) {
	return newDoltStoreForRepoPath(ctx, repoPath, true)
}
