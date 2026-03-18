package doctor

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func openDoltStoreForRepoPath(ctx context.Context, path string) (*dolt.DoltStore, error) {
	runtimeInfo := resolveDoctorRuntimeInfoForRepo(path)
	if runtimeInfo == nil || runtimeInfo.Runtime == nil {
		return nil, fmt.Errorf("repo runtime unavailable")
	}

	cfg := &dolt.Config{
		Path:           runtimeInfo.Runtime.DatabasePath,
		BeadsDir:       runtimeInfo.Runtime.BeadsDir,
		Database:       runtimeInfo.Runtime.Database,
		ServerHost:     runtimeInfo.Runtime.Host,
		ServerPort:     runtimeInfo.Runtime.Port,
		ServerUser:     runtimeInfo.Runtime.User,
		ServerPassword: runtimeInfo.Config.GetDoltServerPassword(),
		ServerTLS:      runtimeInfo.Runtime.TLS,
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
	dolt.ApplyCLIAutoStart(runtimeInfo.Runtime.BeadsDir, cfg)

	return dolt.New(ctx, cfg)
}
