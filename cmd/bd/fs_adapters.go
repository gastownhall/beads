package main

import (
	"context"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/templates/agents"
)

func newBeadsDirTemplates() domain.BeadsDirTemplates {
	return domain.BeadsDirTemplates{
		BeadsGitignore:           doctor.GitignoreTemplate,
		ProjectGitignoreHeader:   doctor.ProjectGitignoreHeader,
		ProjectGitignorePatterns: doctor.ProjectGitignorePatterns,
		Readme:                   BeadsReadmeTemplate,
	}
}

func newFileSystemAdapters() domain.BeadsDirFSAdapters {
	return domain.BeadsDirFSAdapters{
		ApplyNoCOW:        applyNoCOW,
		WriteLocalVersion: writeLocalVersion,
		SetupForkExclude:  setupForkExclude,
		SetupStealthMode:  setupStealthMode,
		InstallGitHooks: func(p domain.HooksInstallParams) error {
			return installHooksWithOptions(p.HookNames, p.Force, p.Shared, p.Chain, p.BeadsHooks)
		},
		InstallJJHooks: installJJHooks,
		AddAgentsInstructions: func(p domain.AgentsFileParams) {
			addAgentsInstructions(p.File, p.Verbose, p.TemplatePath, agents.Profile(p.Profile), agents.RenderOpts{HasRemote: p.HasRemote, NoPush: p.NoPush})
		},
		InstallClaudeProject: setup.InstallClaudeProject,
		SetYAMLConfig:        config.SetYamlConfig,
	}
}

// newInitFileSystemAdapters keeps the selected Git project separate from storage paths.
func newInitFileSystemAdapters(workDir string) domain.BeadsDirFSAdapters {
	adapters := newFileSystemAdapters()
	adapters.SetupForkExclude = func(verbose bool) error {
		return setupForkExcludeAt(workDir, verbose)
	}
	adapters.SetupStealthMode = func(verbose bool) error {
		return setupStealthModeAt(workDir, verbose)
	}
	return adapters
}

// withInitHooks scopes only hook operations; exclude and the other supplied
// callbacks keep their existing provider. Empty paths retain isolated callers.
func withInitHooks(fs domain.BeadsDirFSUseCase, workDir, beadsDir string) (domain.BeadsDirFSUseCase, *initHooksContext, error) {
	if workDir == "" {
		return fs, nil, nil
	}
	hooks, err := resolveInitHooksContext(workDir, beadsDir)
	if err != nil {
		return nil, nil, err
	}
	return initHooksFileSystem{BeadsDirFSUseCase: fs, hooks: hooks}, hooks, nil
}

type initHooksFileSystem struct {
	domain.BeadsDirFSUseCase
	hooks *initHooksContext
}

func (fs initHooksFileSystem) InstallGitHooks(_ context.Context, p domain.HooksInstallParams) error {
	return installHooksWithContext(p.HookNames, p.Force, p.Shared, p.Chain, p.BeadsHooks, fs.hooks)
}

func (fs initHooksFileSystem) InstallJJHooks(_ context.Context) error {
	return installHooksWithContext(jjHookNames, false, false, false, false, fs.hooks)
}
