package main

import (
	"errors"

	"github.com/steveyegge/beads/internal/configfile"
)

// ExitUnsupportedWorkspaceFormat is the exit code for a command refused
// because its workspace declares a format this bd build cannot open: a
// metadata.json graph_mode other than "dependency", or a graph workspace
// format marker (configfile.GraphFormatMarkerFile). Stable so a wrapper can
// tell "this workspace needs a different bd build" apart from a generic
// failure (1) without grepping stderr.
//
// 16 because 13, 14 and 15 are taken (ExitGuardMismatch, ExitMigrationFrozen,
// ExitHomeDirRefused). Like ExitHomeDirRefused, it is not part of protocol v0
// §E3, which freezes 10/11/12/130.
const ExitUnsupportedWorkspaceFormat = 16

// checkWorkspaceFormat refuses metadata whose graph_mode marker names any
// model but the default, and a beadsDir holding the graph format marker file,
// with a *configfile.UnsupportedWorkspaceFormatError. It is
// configfile.CheckWorkspaceFormat, the check the public beads package and the
// Dolt store's metadata-driven opens apply too, so the CLI and a library
// caller refuse the same workspaces with the same error.
func checkWorkspaceFormat(cfg *configfile.Config, beadsDir string) error {
	return configfile.CheckWorkspaceFormat(cfg, beadsDir)
}

// loadAndCheckWorkspaceFormat reads beadsDir's discovery metadata without
// migrating it and refuses a workspace in a format this build cannot open,
// including unreadable metadata beside the graph format marker. It is
// configfile.LoadAndCheckWorkspaceFormat.
func loadAndCheckWorkspaceFormat(beadsDir string) (*configfile.Config, error) {
	return configfile.LoadAndCheckWorkspaceFormat(beadsDir)
}

// hasGraphFormatMarker reports whether anything exists at beadsDir's graph
// format marker path (configfile.HasGraphFormatMarker).
func hasGraphFormatMarker(beadsDir string) bool {
	return configfile.HasGraphFormatMarker(beadsDir)
}

// checkDiscoveredWorkspaceFormat runs checkWorkspaceFormat on beadsDir's
// metadata as LoadForDiscovery reads it, which never rewrites the workspace.
// A path that opens or initializes a workspace without passing through command
// admission runs it first: configfile.Load would otherwise migrate a legacy
// config.json into metadata.json before the format check could refuse it.
// Metadata that does not load is left to the caller's own load to report,
// unless the graph format marker sits beside it (loadAndCheckWorkspaceFormat).
// It is configfile.CheckDiscoveredWorkspaceFormat, the pre-check the public
// beads package's opens run too.
func checkDiscoveredWorkspaceFormat(beadsDir string) error {
	return configfile.CheckDiscoveredWorkspaceFormat(beadsDir)
}

func isUnsupportedWorkspaceFormat(err error) bool {
	var formatErr *configfile.UnsupportedWorkspaceFormatError
	return errors.As(err, &formatErr)
}

// handleWorkspaceGuardError renders a workspace admission refusal from
// guardLegacyUpgradeWorkspace. An unsupported workspace format gets a hint and
// ExitUnsupportedWorkspaceFormat; every other refusal keeps HandleError's
// rendering and exit 1.
func handleWorkspaceGuardError(err error) error {
	if err == nil {
		return nil
	}
	var formatErr *configfile.UnsupportedWorkspaceFormatError
	if !errors.As(err, &formatErr) {
		return HandleError("%v", err)
	}
	_ = HandleErrorWithHint(formatErr.Error(), formatErr.Hint())
	return &exitError{Code: ExitUnsupportedWorkspaceFormat}
}
