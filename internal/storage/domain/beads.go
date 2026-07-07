package domain

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

type BeadsDirFSRepository interface {
	ResolveBeadsDirPath(ctx context.Context) BeadsDirResolution
	BeadsDirIsLocal(ctx context.Context) bool

	CreateBeadsDir(ctx context.Context) error
	BeadsDirExists(ctx context.Context) (bool, error)
	WriteBeadsGitignore(ctx context.Context) error
	BeadsGitignoreExists(ctx context.Context) (bool, error)
	WriteProjectGitignore(ctx context.Context) error
	ProjectGitignoreExists(ctx context.Context) (bool, error)
	WriteInteractionsLog(ctx context.Context) error
	WriteReadme(ctx context.Context) error
	WriteMetadataJSON(ctx context.Context, content []byte) error
	ReadMetadataJSON(ctx context.Context) ([]byte, error)
	WriteConfigYAML(ctx context.Context, content []byte) error
	ReadConfigYAML(ctx context.Context) ([]byte, error)
	ReadBeadsConfig(ctx context.Context) (*configfile.Config, error)
	WriteProxiedServerClientInfo(ctx context.Context, info *configfile.ProxiedServerClientInfo) error
	ReadProxiedServerClientInfo(ctx context.Context) (*configfile.ProxiedServerClientInfo, error)
}

type BeadsDirFSUseCase interface {
	ResolveBeadsDir(ctx context.Context) BeadsDirResolution
	ResolveProxiedInit(ctx context.Context, params ResolveProxiedInitParams) (ResolveProxiedInitResult, error)
	InitializeBeadsDir(ctx context.Context, params InitializeBeadsDirParams) (InitializeBeadsDirResult, error)
	SetupForkExclude(ctx context.Context, verbose bool) error
	SetupStealthMode(ctx context.Context, verbose bool) error
	InstallGitHooks(ctx context.Context, params HooksInstallParams) error
	InstallJJHooks(ctx context.Context) error
	AddAgentsInstructions(ctx context.Context, params AgentsFileParams) error
	InstallClaudeProject(ctx context.Context, stealth bool) error
	SetYAMLConfig(ctx context.Context, key, value string) error
}

type BeadsDirResolution struct {
	BeadsDir    string
	HasExplicit bool
}

type ResolveProxiedInitParams struct {
	Prefix string
	DBFlag string
}

type ResolveProxiedInitResult struct {
	BeadsDir    string
	HasExplicit bool
	IsLocal     bool
	DBName      string
	ProjectID   string
}

type BeadsDirTemplates struct {
	BeadsGitignore           string
	ProjectGitignoreHeader   string
	ProjectGitignorePatterns []string
	Readme                   string
}

type InitializeBeadsDirParams struct {
	MetadataJSONBody        []byte
	ConfigYAMLBody          []byte
	ProxiedServerClientInfo *configfile.ProxiedServerClientInfo
	WriteProjectGitignore   bool
	SetNoCOW                bool
	LocalVersion            string
}

type InitializeBeadsDirResult struct {
	NoCOWErr        error
	LocalVersionErr error
}

type HooksInstallParams struct {
	HookNames  []string
	Force      bool
	Shared     bool
	Chain      bool
	BeadsHooks bool
}

type AgentsFileParams struct {
	File         string
	Verbose      bool
	TemplatePath string
	Profile      string
	HasRemote    bool
	NoPush       bool
}

type BeadsDirFSAdapters struct {
	ApplyNoCOW            func(path string) error
	WriteLocalVersion     func(path, version string) error
	SetupForkExclude      func(verbose bool) error
	SetupStealthMode      func(verbose bool) error
	InstallGitHooks       func(params HooksInstallParams) error
	InstallJJHooks        func() error
	AddAgentsInstructions func(params AgentsFileParams)
	InstallClaudeProject  func(stealth bool) error
	SetYAMLConfig         func(key, value string) error
}

func NewBeadsDirFSUseCase(fsRepo BeadsDirFSRepository, adapters BeadsDirFSAdapters) BeadsDirFSUseCase {
	return &beadsDirFSUseCaseImpl{fsRepo: fsRepo, adapters: adapters}
}

type beadsDirFSUseCaseImpl struct {
	fsRepo   BeadsDirFSRepository
	adapters BeadsDirFSAdapters
}

var _ BeadsDirFSUseCase = (*beadsDirFSUseCaseImpl)(nil)

func (u *beadsDirFSUseCaseImpl) ResolveBeadsDir(ctx context.Context) BeadsDirResolution {
	return u.fsRepo.ResolveBeadsDirPath(ctx)
}

func (u *beadsDirFSUseCaseImpl) ResolveProxiedInit(ctx context.Context, params ResolveProxiedInitParams) (ResolveProxiedInitResult, error) {
	resolution := u.fsRepo.ResolveBeadsDirPath(ctx)
	result := ResolveProxiedInitResult{
		BeadsDir:    resolution.BeadsDir,
		HasExplicit: resolution.HasExplicit,
		IsLocal:     u.fsRepo.BeadsDirIsLocal(ctx),
	}

	cfg, err := u.fsRepo.ReadBeadsConfig(ctx)
	if err != nil {
		return ResolveProxiedInitResult{}, fmt.Errorf("ResolveProxiedInit: read config: %w", err)
	}

	result.DBName = resolveDoltDatabaseName(cfg, params.Prefix, params.DBFlag)
	result.ProjectID = resolveProjectID(cfg)
	return result, nil
}

func resolveDoltDatabaseName(cfg *configfile.Config, prefix, dbFlag string) string {
	if dbFlag != "" {
		return dbFlag
	}
	if cfg != nil && cfg.DoltDatabase != "" {
		return cfg.DoltDatabase
	}
	if prefix != "" {
		return strings.ReplaceAll(prefix, "-", "_")
	}
	return configfile.DefaultDoltDatabase
}

func resolveProjectID(cfg *configfile.Config) string {
	if cfg != nil && cfg.ProjectID != "" {
		return cfg.ProjectID
	}
	return configfile.GenerateProjectID()
}

func (u *beadsDirFSUseCaseImpl) InitializeBeadsDir(ctx context.Context, params InitializeBeadsDirParams) (InitializeBeadsDirResult, error) {
	if err := u.fsRepo.CreateBeadsDir(ctx); err != nil {
		return InitializeBeadsDirResult{}, err
	}
	if err := u.fsRepo.WriteBeadsGitignore(ctx); err != nil {
		return InitializeBeadsDirResult{}, err
	}
	if len(params.MetadataJSONBody) > 0 {
		if err := u.fsRepo.WriteMetadataJSON(ctx, params.MetadataJSONBody); err != nil {
			return InitializeBeadsDirResult{}, err
		}
	}
	if len(params.ConfigYAMLBody) > 0 {
		if err := u.fsRepo.WriteConfigYAML(ctx, params.ConfigYAMLBody); err != nil {
			return InitializeBeadsDirResult{}, err
		}
	}
	if params.ProxiedServerClientInfo != nil {
		if err := u.fsRepo.WriteProxiedServerClientInfo(ctx, params.ProxiedServerClientInfo); err != nil {
			return InitializeBeadsDirResult{}, err
		}
	}
	if err := u.fsRepo.WriteInteractionsLog(ctx); err != nil {
		return InitializeBeadsDirResult{}, err
	}
	if err := u.fsRepo.WriteReadme(ctx); err != nil {
		return InitializeBeadsDirResult{}, err
	}
	if params.WriteProjectGitignore {
		if err := u.fsRepo.WriteProjectGitignore(ctx); err != nil {
			return InitializeBeadsDirResult{}, err
		}
	}

	var result InitializeBeadsDirResult
	if params.SetNoCOW && u.adapters.ApplyNoCOW != nil {
		result.NoCOWErr = u.adapters.ApplyNoCOW(u.fsRepo.ResolveBeadsDirPath(ctx).BeadsDir)
	}
	if params.LocalVersion != "" && u.adapters.WriteLocalVersion != nil {
		beadsDir := u.fsRepo.ResolveBeadsDirPath(ctx).BeadsDir
		result.LocalVersionErr = u.adapters.WriteLocalVersion(
			filepath.Join(beadsDir, ".local_version"),
			params.LocalVersion,
		)
	}
	return result, nil
}

func (u *beadsDirFSUseCaseImpl) SetupForkExclude(ctx context.Context, verbose bool) error {
	if u.adapters.SetupForkExclude == nil {
		return fmt.Errorf("SetupForkExclude: adapter not configured")
	}
	return u.adapters.SetupForkExclude(verbose)
}

func (u *beadsDirFSUseCaseImpl) SetupStealthMode(ctx context.Context, verbose bool) error {
	if u.adapters.SetupStealthMode == nil {
		return fmt.Errorf("SetupStealthMode: adapter not configured")
	}
	return u.adapters.SetupStealthMode(verbose)
}

func (u *beadsDirFSUseCaseImpl) InstallGitHooks(ctx context.Context, params HooksInstallParams) error {
	if u.adapters.InstallGitHooks == nil {
		return fmt.Errorf("InstallGitHooks: adapter not configured")
	}
	return u.adapters.InstallGitHooks(params)
}

func (u *beadsDirFSUseCaseImpl) InstallJJHooks(ctx context.Context) error {
	if u.adapters.InstallJJHooks == nil {
		return fmt.Errorf("InstallJJHooks: adapter not configured")
	}
	return u.adapters.InstallJJHooks()
}

func (u *beadsDirFSUseCaseImpl) AddAgentsInstructions(ctx context.Context, params AgentsFileParams) error {
	if u.adapters.AddAgentsInstructions == nil {
		return fmt.Errorf("AddAgentsInstructions: adapter not configured")
	}
	u.adapters.AddAgentsInstructions(params)
	return nil
}

func (u *beadsDirFSUseCaseImpl) InstallClaudeProject(ctx context.Context, stealth bool) error {
	if u.adapters.InstallClaudeProject == nil {
		return fmt.Errorf("InstallClaudeProject: adapter not configured")
	}
	return u.adapters.InstallClaudeProject(stealth)
}

func (u *beadsDirFSUseCaseImpl) SetYAMLConfig(ctx context.Context, key, value string) error {
	if u.adapters.SetYAMLConfig == nil {
		return fmt.Errorf("SetYAMLConfig: adapter not configured")
	}
	return u.adapters.SetYAMLConfig(key, value)
}
