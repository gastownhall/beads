package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func runContextProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	if selected := selectedNoDBBeadsDir(cmd); selected != "" {
		prepareSelectedNoDBContext(selected)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return contextProxiedError("cannot resolve working directory: %v", err)
	}

	info, err := contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(ctx)
	if err != nil {
		return contextProxiedError("cannot resolve context: %v", err)
	}

	view := contextInfoView(info)
	if jsonOutput {
		return outputJSON(view)
	}
	printContextText(view)
	return nil
}

func contextProxiedError(format string, args ...any) error {
	if jsonOutput {
		if jerr := outputJSON(map[string]string{"error": fmt.Sprintf(format, args...)}); jerr != nil {
			return jerr
		}
		return SilentExit()
	}
	return HandleError(format, args...)
}

func contextInfoView(d domain.ContextInfo) ContextInfo {
	return ContextInfo{
		BeadsDir:      d.BeadsDir,
		RepoRoot:      d.RepoRoot,
		CWDRepoRoot:   d.CWDRepoRoot,
		IsRedirected:  d.IsRedirected,
		IsWorktree:    d.IsWorktree,
		Backend:       d.Backend,
		DoltMode:      d.DoltMode,
		ServerHost:    d.ServerHost,
		ServerPort:    d.ServerPort,
		ProxiedDir:    d.ProxiedDir,
		Database:      d.Database,
		DataDir:       d.DataDir,
		ProjectID:     d.ProjectID,
		SyncRemote:    d.SyncRemote,
		SyncGitRemote: d.SyncRemote,
		Role:          d.Role,
		BdVersion:     d.BdVersion,
	}
}
