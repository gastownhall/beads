package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
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

	configMap, err := uw.ConfigUseCase().GetAllConfig(ctx)
	if err == nil && len(configMap) > 0 {
		info["config"] = configMap
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
