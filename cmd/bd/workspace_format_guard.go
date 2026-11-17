package main

import (
	"github.com/steveyegge/beads/internal/configfile"
)

// checkDiscoveredForOpen is configfile.CheckDiscoveredForOpen, the
// non-mutating pre-check every path that opens, attaches or initializes a
// workspace without passing through command admission runs before it writes
// there: configfile.Load would otherwise migrate a legacy config.json into
// metadata.json before the workspace could be refused. It refuses what
// admission refuses from the workspace's own metadata and layout, with
// admission's text: a historical SQLite workspace
// (*configfile.LegacyWorkspaceError). Metadata that does not load is left to
// the caller's own load to report. The public beads package's opens run the
// same check.
func checkDiscoveredForOpen(beadsDir string) error {
	return configfile.CheckDiscoveredForOpen(beadsDir)
}

// handleWorkspaceGuardError renders a workspace admission refusal from
// guardLegacyUpgradeWorkspace, or the same refusal of a workspace bd was not
// launched in, with HandleError's rendering and exit 1.
func handleWorkspaceGuardError(err error) error {
	if err == nil {
		return nil
	}
	return HandleError("%v", err)
}
