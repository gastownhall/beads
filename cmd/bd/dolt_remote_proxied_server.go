package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func runDoltRemoteRemoveProxied(ctx context.Context, name string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	if err := uw.DoltRemoteUseCase().DeleteRemote(ctx, name); err != nil {
		if jsonOutput {
			_ = outputJSONError(err, "remote_remove_failed")
		} else {
			fmt.Fprintf(os.Stderr, "Error removing remote: %v\n", err)
		}
		return SilentExit()
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: remove remote %s", name)); err != nil && !isDoltNothingToCommit(err) {
		return HandleErrorRespectJSON("commit: %v", err)
	}

	if name == "origin" {
		if current := config.GetYamlConfig("sync.remote"); current != "" {
			if err := config.UnsetYamlConfig("sync.remote"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to clear sync.remote from config.yaml: %v\n", err)
			}
			if isGitRepo() {
				commitBeadsConfig("bd: clear sync.remote")
			}
		}
	}

	if jsonOutput {
		if err := outputJSON(map[string]interface{}{
			"name":    name,
			"removed": true,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	} else {
		fmt.Printf("Removed remote %q\n", name)
	}
	return nil
}
