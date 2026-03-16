package fix

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		Runtime:      beads.BuildFallbackRepoRuntime(repoPath, sourceBeadsDir, beadsDir, cfgEffective),
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

func fallbackRuntimeInfoForRepoReinit(repoPath string) *repoRuntimeInfo {
	sourceBeadsDir := filepath.Join(repoPath, ".beads")
	beadsDir := resolveBeadsDir(sourceBeadsDir)
	cfg := configfile.DefaultConfig()

	return &repoRuntimeInfo{
		Runtime:      beads.BuildFallbackRepoRuntime(repoPath, sourceBeadsDir, beadsDir, cfg),
		Config:       cfg,
		SourceConfig: cfg,
	}
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

func selectedRuntimeDatabase(runtime *beads.RepoRuntime, cfg *configfile.Config) string {
	if runtime != nil && runtime.Database != "" {
		return runtime.Database
	}
	cfg = effectiveFixConfig(cfg)
	if database := cfg.GetDoltDatabase(); database != "" {
		return database
	}
	return configfile.DefaultDoltDatabase
}

func runtimeDatabaseDir(runtime *beads.RepoRuntime) string {
	if runtime == nil || runtime.DatabasePath == "" {
		return ""
	}
	database := selectedRuntimeDatabase(runtime, nil)
	if database == "" {
		return ""
	}
	return filepath.Join(runtime.DatabasePath, database)
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

func openFixAdminDBForRuntime(runtime *beads.RepoRuntime, cfg *configfile.Config) (*sql.DB, error) {
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
	port := runtime.Port
	if port == 0 && runtime.BeadsDir != "" && (host == configfile.DefaultDoltServerHost || host == "localhost") {
		ensuredPort, err := doltserver.EnsureRunning(runtime.BeadsDir)
		if err == nil {
			port = ensuredPort
		}
	}
	if port == 0 && runtime.BeadsDir != "" {
		port = doltserver.DefaultConfig(runtime.BeadsDir).Port
	}
	if port == 0 {
		return nil, fmt.Errorf("no Dolt server port configured and no server running")
	}

	password := cfg.GetDoltServerPassword()
	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, password, host, port)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, host, port)
	}

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
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

func databaseExistsForRuntime(ctx context.Context, runtime *beads.RepoRuntime, cfg *configfile.Config) (bool, error) {
	if runtime == nil {
		return false, fmt.Errorf("runtime required")
	}

	adminDB, err := openFixAdminDBForRuntime(runtime, cfg)
	if err == nil {
		defer func() { _ = adminDB.Close() }()
		return fixDatabaseExistsOnServer(ctx, adminDB, selectedRuntimeDatabase(runtime, cfg))
	}

	dbDir := runtimeDatabaseDir(runtime)
	if dbDir != "" {
		if _, statErr := os.Stat(dbDir); statErr == nil {
			return true, nil
		} else if !os.IsNotExist(statErr) {
			return false, statErr
		}
	}
	return false, err
}

func dropRuntimeDatabase(ctx context.Context, runtime *beads.RepoRuntime, cfg *configfile.Config) error {
	if runtime == nil {
		return fmt.Errorf("runtime required")
	}

	database := selectedRuntimeDatabase(runtime, cfg)
	if err := dolt.ValidateDatabaseName(database); err != nil {
		return err
	}

	adminDB, err := openFixAdminDBForRuntime(runtime, cfg)
	if err == nil {
		defer func() { _ = adminDB.Close() }()
		safeName := strings.ReplaceAll(database, "`", "``")
		if _, execErr := adminDB.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", safeName)); execErr != nil {
			return fmt.Errorf("drop database %q: %w", database, execErr)
		}
		return nil
	}

	dbDir := runtimeDatabaseDir(runtime)
	if dbDir == "" {
		return err
	}
	if rmErr := os.RemoveAll(dbDir); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("remove database dir %q: %w", dbDir, rmErr)
	}
	return nil
}

func fixDatabaseExistsOnServer(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return false, err
		}
		if dbName == name {
			return true, nil
		}
	}
	return false, rows.Err()
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

func newDoltStoreForRuntime(ctx context.Context, runtime *beads.RepoRuntime, cfgFile *configfile.Config, createIfMissing bool) (*dolt.DoltStore, error) {
	if runtime == nil {
		return nil, fmt.Errorf("repo runtime unavailable")
	}
	cfgFile = effectiveFixConfig(cfgFile)
	cfg := &dolt.Config{
		Path:            runtime.DatabasePath,
		BeadsDir:        runtime.BeadsDir,
		Database:        selectedRuntimeDatabase(runtime, cfgFile),
		ServerHost:      runtime.Host,
		ServerPort:      runtime.Port,
		ServerUser:      runtime.User,
		ServerPassword:  cfgFile.GetDoltServerPassword(),
		ServerTLS:       runtime.TLS,
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
	dolt.ApplyCLIAutoStart(runtime.BeadsDir, cfg)

	store, err := dolt.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func newDoltStoreForRepoPath(ctx context.Context, repoPath string, createIfMissing bool) (*dolt.DoltStore, error) {
	info, err := resolveRuntimeInfoForRepo(repoPath)
	if err != nil {
		return nil, err
	}
	return newDoltStoreForRuntime(ctx, info.Runtime, info.Config, createIfMissing)
}

func openDoltStoreForRepoPath(ctx context.Context, repoPath string) (*dolt.DoltStore, error) {
	return newDoltStoreForRepoPath(ctx, repoPath, false)
}

func createDoltStoreForRepoPath(ctx context.Context, repoPath string) (*dolt.DoltStore, error) {
	return newDoltStoreForRepoPath(ctx, repoPath, true)
}

func createDoltStoreForRuntime(ctx context.Context, runtime *beads.RepoRuntime, cfg *configfile.Config) (*dolt.DoltStore, error) {
	return newDoltStoreForRuntime(ctx, runtime, cfg, true)
}
