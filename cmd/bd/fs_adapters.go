package main

import (
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
