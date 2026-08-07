package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// listInput is everything `bd list` parsed off the command line: the
// frontend-independent query knobs (issueops.ListRequest, the request the
// reader role takes and the filter is built from) plus the presentation
// choices that never leave the CLI.
type listInput struct {
	issueops.ListRequest

	longFormat   bool
	prettyFormat bool
	flatFormat   bool
	depsMode     string
	watchMode    bool
	noPager      bool
	formatStr    string
	jsonOutput   bool

	limitChanged   bool
	effectiveLimit int

	repoOverride    string
	repoOverrideSet bool
}

func gatherListInput(cmd *cobra.Command) (listInput, error) {
	in := listInput{}

	in.Status, _ = cmd.Flags().GetString("status")
	if in.Status == "" {
		in.Status, _ = cmd.Flags().GetString("state")
	}

	in.Assignee, _ = cmd.Flags().GetString("assignee")
	rawType, _ := cmd.Flags().GetString("type")
	in.IssueType = utils.NormalizeIssueType(rawType)

	limit, _ := cmd.Flags().GetInt("limit")
	in.limitChanged = cmd.Flags().Changed("limit")
	listLimitConfigured := false
	if !in.limitChanged {
		listLimitConfigured = config.GetValueSource("list.limit") != config.SourceDefault
		limit = config.GetInt("list.limit")
	}
	in.AllFlag, _ = cmd.Flags().GetBool("all")

	in.formatStr, _ = cmd.Flags().GetString("format")
	if strings.EqualFold(in.formatStr, "json") {
		jsonOutput = true
		in.formatStr = ""
	}
	in.jsonOutput = jsonOutput

	in.Labels, _ = cmd.Flags().GetStringSlice("label")
	in.LabelsAny, _ = cmd.Flags().GetStringSlice("label-any")
	in.ExcludeLabels, _ = cmd.Flags().GetStringSlice("exclude-label")
	in.LabelPattern, _ = cmd.Flags().GetString("label-pattern")
	in.LabelRegex, _ = cmd.Flags().GetString("label-regex")
	in.TitleSearch, _ = cmd.Flags().GetString("title")
	in.SpecPrefix, _ = cmd.Flags().GetString("spec")
	in.IDFilter, _ = cmd.Flags().GetString("id")
	in.longFormat, _ = cmd.Flags().GetBool("long")
	in.SortBy, _ = cmd.Flags().GetString("sort")
	in.Reverse, _ = cmd.Flags().GetBool("reverse")

	in.TitleContains, _ = cmd.Flags().GetString("title-contains")
	in.DescContains, _ = cmd.Flags().GetString("desc-contains")
	in.NotesContains, _ = cmd.Flags().GetString("notes-contains")
	in.ExternalContains, _ = cmd.Flags().GetString("external-contains")
	in.ExternalRef, _ = cmd.Flags().GetString("external-ref")

	in.EmptyDesc, _ = cmd.Flags().GetBool("empty-description")
	in.NoAssignee, _ = cmd.Flags().GetBool("no-assignee")
	in.NoLabels, _ = cmd.Flags().GetBool("no-labels")

	in.SkipLabels, _ = cmd.Flags().GetBool("skip-labels")
	if in.SkipLabels {
		conflicts := skipLabelsConflicts(in.Labels, in.LabelsAny, in.LabelPattern, in.LabelRegex, in.ExcludeLabels, in.NoLabels)
		if len(conflicts) > 0 {
			fmt.Fprint(os.Stderr, formatSkipLabelsConflictError(conflicts))
			return in, &exitError{Code: 2}
		}
	}

	if cmd.Flags().Changed("priority") {
		priorityStr, _ := cmd.Flags().GetString("priority")
		p, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return in, HandleError("%v", err)
		}
		in.Priority = &p
	}
	if cmd.Flags().Changed("priority-min") {
		s, _ := cmd.Flags().GetString("priority-min")
		p, err := validation.ValidatePriority(s)
		if err != nil {
			return in, HandleError("parsing --priority-min: %v", err)
		}
		in.PriorityMin = &p
	}
	if cmd.Flags().Changed("priority-max") {
		s, _ := cmd.Flags().GetString("priority-max")
		p, err := validation.ValidatePriority(s)
		if err != nil {
			return in, HandleError("parsing --priority-max: %v", err)
		}
		in.PriorityMax = &p
	}

	in.PinnedFlag, _ = cmd.Flags().GetBool("pinned")
	in.NoPinnedFlag, _ = cmd.Flags().GetBool("no-pinned")
	if in.PinnedFlag && in.NoPinnedFlag {
		return in, HandleError("--pinned and --no-pinned are mutually exclusive")
	}

	in.IncludeTemplates, _ = cmd.Flags().GetBool("include-templates")
	in.IncludeGates, _ = cmd.Flags().GetBool("include-gates")
	in.IncludeInfra, _ = cmd.Flags().GetBool("include-infra")
	in.ExcludeTypes, _ = cmd.Flags().GetStringSlice("exclude-type")

	in.ParentID, _ = cmd.Flags().GetString("parent")
	if in.ParentID == "" {
		in.ParentID, _ = cmd.Flags().GetString("filter-parent")
	}
	in.NoParent, _ = cmd.Flags().GetBool("no-parent")
	if in.ParentID != "" && in.NoParent {
		return in, HandleError("--parent and --no-parent are mutually exclusive")
	}

	if s, _ := cmd.Flags().GetString("mol-type"); s != "" {
		mt := types.MolType(s)
		if !mt.IsValid() {
			return in, HandleError("invalid mol-type %q (must be %s)", s, types.ValidMolTypeNames())
		}
		in.MolType = &mt
	}
	if s, _ := cmd.Flags().GetString("wisp-type"); s != "" {
		wt := types.WispType(s)
		if !wt.IsValid() {
			return in, HandleError("invalid wisp-type %q (must be %s)", s, types.ValidWispTypeNames())
		}
		in.WispType = &wt
	}

	in.DeferredFlag, _ = cmd.Flags().GetBool("deferred")
	in.OverdueFlag, _ = cmd.Flags().GetBool("overdue")

	var err error
	if in.CreatedAfter, err = parseListTimeFlag(cmd, "created-after"); err != nil {
		return in, err
	}
	if in.CreatedBefore, err = parseListTimeFlag(cmd, "created-before"); err != nil {
		return in, err
	}
	if in.UpdatedAfter, err = parseListTimeFlag(cmd, "updated-after"); err != nil {
		return in, err
	}
	if in.UpdatedBefore, err = parseListTimeFlag(cmd, "updated-before"); err != nil {
		return in, err
	}
	if in.ClosedAfter, err = parseListTimeFlag(cmd, "closed-after"); err != nil {
		return in, err
	}
	if in.ClosedBefore, err = parseListTimeFlag(cmd, "closed-before"); err != nil {
		return in, err
	}
	if in.DeferAfter, err = parseListTimeFlag(cmd, "defer-after"); err != nil {
		return in, err
	}
	if in.DeferBefore, err = parseListTimeFlag(cmd, "defer-before"); err != nil {
		return in, err
	}
	if in.DueAfter, err = parseListTimeFlag(cmd, "due-after"); err != nil {
		return in, err
	}
	if in.DueBefore, err = parseListTimeFlag(cmd, "due-before"); err != nil {
		return in, err
	}

	metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
	if len(metadataFieldFlags) > 0 {
		in.MetadataFields = make(map[string]string, len(metadataFieldFlags))
		for _, mf := range metadataFieldFlags {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return in, HandleErrorRespectJSON("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return in, HandleErrorRespectJSON("invalid --metadata-field key: %v", err)
			}
			in.MetadataFields[k] = v
		}
	}
	if k, _ := cmd.Flags().GetString("has-metadata-key"); k != "" {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return in, HandleErrorRespectJSON("invalid --has-metadata-key: %v", err)
		}
		in.HasMetadataKey = k
	}

	prettyFormat, _ := cmd.Flags().GetBool("pretty")
	treeFormat, _ := cmd.Flags().GetBool("tree")
	in.flatFormat, _ = cmd.Flags().GetBool("flat")
	if in.flatFormat {
		treeFormat = false
	}
	in.prettyFormat = (prettyFormat || treeFormat) && !in.jsonOutput && in.formatStr == ""
	in.watchMode, _ = cmd.Flags().GetBool("watch")
	if in.watchMode {
		in.prettyFormat = true
	}
	in.noPager, _ = cmd.Flags().GetBool("no-pager")
	in.ReadyFlag, _ = cmd.Flags().GetBool("ready")

	in.depsMode, _ = cmd.Flags().GetString("deps")
	if in.depsMode != "" {
		if in.depsMode != "scheduling" && in.depsMode != "all" {
			return in, HandleErrorRespectJSON("invalid --deps value %q (valid: scheduling, all)", in.depsMode)
		}
		// --deps annotates and orders the parent-child tree, so it is meaningful
		// only in the tree view. Reject the non-tree output modes rather than
		// accept the flag and silently ignore it, then imply the tree view so a
		// bare `--deps` renders as intended (mirrors --watch implying --pretty).
		switch {
		case in.jsonOutput:
			return in, HandleErrorRespectJSON("--deps is not supported with --json output")
		case in.formatStr != "":
			return in, HandleErrorRespectJSON("--deps is not supported with --format output")
		case in.flatFormat:
			return in, HandleErrorRespectJSON("--deps requires the tree view and cannot be combined with --flat")
		case in.watchMode:
			return in, HandleErrorRespectJSON("--deps is not supported with --watch")
		}
		in.prettyFormat = true
	}

	if in.SortBy != "" {
		validSortFields := map[string]bool{
			"priority": true, "created": true, "updated": true, "closed": true,
			"status": true, "id": true, "title": true, "type": true, "assignee": true,
		}
		if !validSortFields[in.SortBy] {
			return in, HandleError("invalid sort field %q (valid: priority, created, updated, closed, status, id, title, type, assignee)", in.SortBy)
		}
	}

	in.Labels = utils.NormalizeLabels(in.Labels)
	in.LabelsAny = utils.NormalizeLabels(in.LabelsAny)
	in.ExcludeLabels = utils.NormalizeLabels(in.ExcludeLabels)

	if !in.SkipLabels && len(in.Labels) == 0 && len(in.LabelsAny) == 0 {
		if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
			in.LabelsAny = dirLabels
		}
	}

	in.effectiveLimit = limit
	switch {
	case in.limitChanged:
		in.effectiveLimit = limit
	case in.AllFlag:
		in.effectiveLimit = 0
	case listLimitConfigured:
		in.effectiveLimit = limit
	case !ui.IsTerminal():
		in.effectiveLimit = 0 // Piped stdout should not truncate (GH#4094)
	case ui.IsAgentMode():
		in.effectiveLimit = 20
	}
	// The request carries the limit the caller receives. Which row limit that
	// implies for the query - a sort SQL cannot express fetches everything and
	// trims client-side - is workapi.SQLLimit's decision, made once, inside the
	// builder, for every frontend.
	pageLimit := in.effectiveLimit
	in.Limit = &pageLimit

	if cmd.Flags().Changed("offset") {
		offset, _ := cmd.Flags().GetInt("offset")
		if offset < 0 {
			return in, HandleError("--offset must be >= 0")
		}
		// --offset only makes sense when pagination happens in SQL. Sorts
		// that fall back to Go-side (currently --sort id) fetch everything
		// regardless, so combining them with --offset is misleading — the
		// caller would think they're paging when they're really pulling
		// the whole result set.
		if offset > 0 && workapi.SQLLimit(in.ListRequest) == 0 && in.SortBy == "id" {
			return in, HandleError("--offset is not supported with --sort %s (sort requires fetching the full result set)", in.SortBy)
		}
		in.Offset = offset
	}

	// The defensive cap is part of the REQUEST, not something stamped onto the
	// filter after the builder has produced it (issueops.ListRequest.MaxRows).
	//
	// Resolving it HERE also means it is resolved exactly once per invocation.
	// resolveMaxRowsEnvOnly warns on a malformed BEADS_MAX_ROWS every time it
	// runs, so runListCore's proxied-server refusal reads the value this
	// resolved (rejectResolvedMaxRowsUnderProxiedServer) rather than resolving
	// a second time and warning twice.
	maxRows, maxRowsSource, err := resolveMaxRows(cmd)
	if err != nil {
		return in, err
	}
	in.MaxRows = maxRows
	in.MaxRowsSource = maxRowsSource

	in.repoOverride, _ = cmd.Flags().GetString("repo")
	in.repoOverrideSet = cmd.Flags().Changed("repo")

	return in, nil
}

func parseListTimeFlag(cmd *cobra.Command, name string) (*time.Time, error) {
	s, _ := cmd.Flags().GetString(name)
	if s == "" {
		return nil, nil
	}
	t, err := parseTimeFlag(s)
	if err != nil {
		return nil, HandleError("parsing --%s: %v", name, err)
	}
	return &t, nil
}
