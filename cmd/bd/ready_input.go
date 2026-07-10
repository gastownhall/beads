package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

type readyInput struct {
	filter       types.WorkFilter
	limit        int
	offset       int
	claim        bool
	gated        bool
	molID        string
	explain      bool
	prettyFormat bool
	plainFormat  bool
	parentID     string
	jsonOut      bool
}

func gatherReadyInput(cmd *cobra.Command) (readyInput, error) {
	in := readyInput{}

	in.claim, _ = cmd.Flags().GetBool("claim")
	in.gated, _ = cmd.Flags().GetBool("gated")
	in.molID, _ = cmd.Flags().GetString("mol")
	in.explain, _ = cmd.Flags().GetBool("explain")
	in.prettyFormat, _ = cmd.Flags().GetBool("pretty")
	in.plainFormat, _ = cmd.Flags().GetBool("plain")
	in.jsonOut = jsonOutput

	in.limit, _ = cmd.Flags().GetInt("limit")
	if cmd.Flags().Changed("offset") {
		offset, _ := cmd.Flags().GetInt("offset")
		if offset < 0 {
			return in, HandleError("--offset must be >= 0")
		}
		in.offset = offset
	}
	assignee, _ := cmd.Flags().GetString("assignee")
	unassigned, _ := cmd.Flags().GetBool("unassigned")
	sortPolicy, _ := cmd.Flags().GetString("sort")
	labels, _ := cmd.Flags().GetStringSlice("label")
	labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
	excludeLabels, _ := cmd.Flags().GetStringSlice("exclude-label")
	issueType, _ := cmd.Flags().GetString("type")
	issueType = utils.NormalizeIssueType(issueType)
	in.parentID, _ = cmd.Flags().GetString("parent")
	molTypeStr, _ := cmd.Flags().GetString("mol-type")
	includeDeferred, _ := cmd.Flags().GetBool("include-deferred")
	includeEphemeral, _ := cmd.Flags().GetBool("include-ephemeral")
	excludeTypeStrs, _ := cmd.Flags().GetStringSlice("exclude-type")

	var molType *types.MolType
	if molTypeStr != "" {
		mt := types.MolType(molTypeStr)
		if !mt.IsValid() {
			return in, HandleError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
		}
		molType = &mt
	}

	if in.claim && assignee != "" {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --assignee")
	}
	if in.claim && in.gated {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --gated")
	}
	if in.claim && in.molID != "" {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --mol")
	}
	if in.claim && in.explain {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --explain")
	}
	if in.offset > 0 && in.claim {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --claim")
	}
	if in.offset > 0 && in.gated {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --gated")
	}
	if in.offset > 0 && in.molID != "" {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --mol")
	}
	if in.offset > 0 && in.explain {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --explain")
	}

	labels = utils.NormalizeLabels(labels)
	labelsAny = utils.NormalizeLabels(labelsAny)
	excludeLabels = utils.NormalizeLabels(excludeLabels)

	if len(labels) == 0 && len(labelsAny) == 0 {
		if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
			labelsAny = dirLabels
		}
	}

	var excludeTypes []types.IssueType
	for _, raw := range excludeTypeStrs {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				excludeTypes = append(excludeTypes, types.IssueType(utils.NormalizeIssueType(t)))
			}
		}
	}

	in.filter = types.WorkFilter{
		Status:           "open",
		Type:             issueType,
		Limit:            in.limit,
		Offset:           in.offset,
		Unassigned:       unassigned,
		SortPolicy:       types.SortPolicy(sortPolicy),
		Labels:           labels,
		LabelsAny:        labelsAny,
		ExcludeLabels:    excludeLabels,
		IncludeDeferred:  includeDeferred,
		IncludeEphemeral: includeEphemeral,
		ExcludeTypes:     excludeTypes,
	}
	if cmd.Flags().Changed("priority") {
		priority, _ := cmd.Flags().GetInt("priority")
		in.filter.Priority = &priority
	}
	if assignee != "" && !unassigned {
		in.filter.Assignee = &assignee
	}
	if in.parentID != "" {
		in.filter.ParentID = &in.parentID
	}
	if molType != nil {
		in.filter.MolType = molType
	}

	metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
	if len(metadataFieldFlags) > 0 {
		in.filter.MetadataFields = make(map[string]string, len(metadataFieldFlags))
		for _, mf := range metadataFieldFlags {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return in, HandleError("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return in, HandleError("invalid --metadata-field key: %v", err)
			}
			in.filter.MetadataFields[k] = v
		}
	}
	hasMetadataKey, _ := cmd.Flags().GetString("has-metadata-key")
	if hasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(hasMetadataKey); err != nil {
			return in, HandleError("invalid --has-metadata-key: %v", err)
		}
		in.filter.HasMetadataKey = hasMetadataKey
	}

	if !in.filter.SortPolicy.IsValid() {
		return in, HandleError("invalid sort policy '%s'. Valid values: hybrid, priority, oldest", sortPolicy)
	}

	return in, nil
}
