package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/versioncheck"
)

func runBeadsVersionSyncCheck(root string) CheckResult {
	return runBeadsVersionSyncCheckWithUV(
		root,
		versioncheck.CheckUVLockFreshness,
	)
}

func runBeadsVersionSyncCheckWithUV(
	root string,
	checkUVLock versioncheck.UVLockChecker,
) CheckResult {
	report, err := versioncheck.Check(root)
	if err != nil {
		output := err.Error()
		if report.CanonicalVersion != "" {
			output += "\nRun: scripts/update-versions.sh " + report.CanonicalVersion
		}
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Output:  output,
			Command: versioncheck.CommandDescription,
		}
	}
	uvAvailable := false
	if checkUVLock != nil {
		var uvErr error
		uvAvailable, uvErr = checkUVLock(root)
		if uvAvailable && uvErr != nil {
			return CheckResult{
				Name:    "Version sync",
				Passed:  false,
				Output:  "MCP uv.lock: stale — run: uv lock --directory integrations/beads-mcp",
				Command: versioncheck.CommandDescription,
			}
		}
	}
	output := report.SuccessMessage()
	if uvAvailable {
		output += "\nMCP uv.lock: fresh (uv lock --check)"
	}
	return CheckResult{
		Name:    "Version sync",
		Passed:  true,
		Output:  output,
		Command: versioncheck.CommandDescription,
	}
}

func resolveBeadsVersionRoot(start string) (string, bool, CheckResult) {
	root, found, err := versioncheck.FindRoot(start)
	if err == nil {
		return root, found, CheckResult{}
	}
	return "", false, CheckResult{
		Name:    "Version sync",
		Passed:  false,
		Output:  fmt.Sprintf("Cannot identify Beads release root: %v", err),
		Command: versioncheck.CommandDescription,
	}
}
