package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

func runInfoProxiedServer(ctx context.Context, schemaFlag bool) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	absDBPath := absoluteDBPath()

	info := map[string]interface{}{
		"database_path": absDBPath,
		"mode":          "proxied-server",
	}

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{})
	var issues []*types.Issue
	if err == nil {
		issues = page.Items
		info["issue_count"] = len(issues)
	}

	// THE SAME FILTER THE SETTINGS ROLE USES. `bd info --json` serves
	// this map whole, and the beads MCP server's get_schema_info tool
	// runs `bd info --schema --json` and returns the parsed dict —
	// config included — so every memory key AND VALUE landed in the
	// transcript of any agent that asked a SCHEMA question. `bd info`
	// is also the diagnostic people paste into bug reports.
	//
	// Unlike `bd config show`, which an operator asks for by name to
	// see provenance, nothing here says "show me my memories".
	configMap, err := uw.ConfigUseCase().GetAllConfig(ctx)
	if err == nil {
		if filtered := workapi.FilterSettingsEnumeration(configMap); len(filtered) > 0 {
			info["config"] = filtered
		}
	}

	if schemaFlag {
		schemaVersion, err := uw.ConfigUseCase().GetLocalMetadata(ctx, "bd_version")
		if err != nil {
			schemaVersion = "unknown"
		}
		prefix, _ := uw.ConfigUseCase().GetConfig(ctx, "issue_prefix")
		info["schema"] = buildInfoSchema(schemaVersion, prefix, issues)
	}

	return renderInfo(info, schemaFlag, absDBPath)
}
