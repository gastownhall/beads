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

// contextInfoView projects a workspace snapshot onto what `bd context` prints
// and marshals. BOTH routes reach it — the proxied one above and the direct
// one in context_cmd.go — so there is one answer to "what does bd context
// say", assembled once.
//
// The workspace identity half comes from domain.PublishedContext, the same
// projection GET /v0/beads/context serves, so the two surfaces cannot name the
// same workspace differently.
//
// Everything below that call is LOCAL-ONLY and deliberately so, which is why
// it is written out here rather than added to the shared projection. These are
// operator diagnostics printed to the terminal of whoever owns the workspace:
// the redirect and worktree flags, the absolute host paths, the database bind
// endpoint, the role, and the sync remote — which is exactly the member the
// shared projection has no room for, because a remote URL routinely embeds a
// credential and an HTTP client is not the person who configured it.
func contextInfoView(d domain.ContextInfo) ContextInfo {
	published := domain.PublishedContext(d)
	return ContextInfo{
		BdVersion: published.BdVersion,
		Backend:   published.Backend,
		DoltMode:  published.DoltMode,
		Database:  published.Database,
		BeadsDir:  published.BeadsDir,
		RepoRoot:  published.RepoRoot,
		ProjectID: published.ProjectID,

		CWDRepoRoot:   d.CWDRepoRoot,
		IsRedirected:  d.IsRedirected,
		IsWorktree:    d.IsWorktree,
		ServerHost:    d.ServerHost,
		ServerPort:    d.ServerPort,
		ProxiedDir:    d.ProxiedDir,
		DataDir:       d.DataDir,
		Role:          d.Role,
		SyncRemote:    d.SyncRemote,
		SyncGitRemote: d.SyncRemote,
	}
}
