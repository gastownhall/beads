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
)

type listInput struct {
	status      string
	issueType   string
	assignee    string
	titleSearch string
	specPrefix  string
	idFilter    string

	labels        []string
	labelsAny     []string
	excludeLabels []string
	labelPattern  string
	labelRegex    string

	titleContains string
	descContains  string
	notesContains string

	createdBefore *time.Time
	createdAfter  *time.Time
	updatedAfter  *time.Time
	updatedBefore *time.Time
	closedAfter   *time.Time
	closedBefore  *time.Time
	deferAfter    *time.Time
	deferBefore   *time.Time
	dueAfter      *time.Time
	dueBefore     *time.Time

	emptyDesc  bool
	noAssignee bool
	noLabels   bool
	skipLabels bool

	priority       int
	prioritySet    bool
	priorityMin    int
	priorityMinSet bool
	priorityMax    int
	priorityMaxSet bool

	pinnedFlag       bool
	noPinnedFlag     bool
	includeTemplates bool
	includeGates     bool
	includeInfra     bool
	excludeTypeStrs  []string

	parentID string
	noParent bool
	molType  *types.MolType
	wispType *types.WispType

	deferredFlag bool
	overdueFlag  bool

	metadataFields map[string]string
	hasMetadataKey string

	allFlag      bool
	readyFlag    bool
	longFormat   bool
	prettyFormat bool
	flatFormat   bool
	watchMode    bool
	noPager      bool
	formatStr    string
	jsonOutput   bool
	sortBy       string
	reverse      bool

	limitChanged   bool
	effectiveLimit int
	sqlLimit       int

	offset int // 0-based starting offset; honored under --proxied-server only.

	repoOverride    string
	repoOverrideSet bool
}

func gatherListInput(cmd *cobra.Command) (listInput, error) {
	in := listInput{}

	in.status, _ = cmd.Flags().GetString("status")
	if in.status == "" {
		in.status, _ = cmd.Flags().GetString("state")
	}

	in.assignee, _ = cmd.Flags().GetString("assignee")
	rawType, _ := cmd.Flags().GetString("type")
	in.issueType = utils.NormalizeIssueType(rawType)

	limit, _ := cmd.Flags().GetInt("limit")
	in.limitChanged = cmd.Flags().Changed("limit")
	in.allFlag, _ = cmd.Flags().GetBool("all")

	in.formatStr, _ = cmd.Flags().GetString("format")
	if strings.EqualFold(in.formatStr, "json") {
		jsonOutput = true
		in.formatStr = ""
	}
	in.jsonOutput = jsonOutput

	in.labels, _ = cmd.Flags().GetStringSlice("label")
	in.labelsAny, _ = cmd.Flags().GetStringSlice("label-any")
	in.excludeLabels, _ = cmd.Flags().GetStringSlice("exclude-label")
	in.labelPattern, _ = cmd.Flags().GetString("label-pattern")
	in.labelRegex, _ = cmd.Flags().GetString("label-regex")
	in.titleSearch, _ = cmd.Flags().GetString("title")
	in.specPrefix, _ = cmd.Flags().GetString("spec")
	in.idFilter, _ = cmd.Flags().GetString("id")
	in.longFormat, _ = cmd.Flags().GetBool("long")
	in.sortBy, _ = cmd.Flags().GetString("sort")
	in.reverse, _ = cmd.Flags().GetBool("reverse")

	in.titleContains, _ = cmd.Flags().GetString("title-contains")
	in.descContains, _ = cmd.Flags().GetString("desc-contains")
	in.notesContains, _ = cmd.Flags().GetString("notes-contains")

	in.emptyDesc, _ = cmd.Flags().GetBool("empty-description")
	in.noAssignee, _ = cmd.Flags().GetBool("no-assignee")
	in.noLabels, _ = cmd.Flags().GetBool("no-labels")

	in.skipLabels, _ = cmd.Flags().GetBool("skip-labels")
	if in.skipLabels {
		conflicts := skipLabelsConflicts(in.labels, in.labelsAny, in.labelPattern, in.labelRegex, in.excludeLabels, in.noLabels)
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
		in.priority = p
		in.prioritySet = true
	}
	if cmd.Flags().Changed("priority-min") {
		s, _ := cmd.Flags().GetString("priority-min")
		p, err := validation.ValidatePriority(s)
		if err != nil {
			return in, HandleError("parsing --priority-min: %v", err)
		}
		in.priorityMin = p
		in.priorityMinSet = true
	}
	if cmd.Flags().Changed("priority-max") {
		s, _ := cmd.Flags().GetString("priority-max")
		p, err := validation.ValidatePriority(s)
		if err != nil {
			return in, HandleError("parsing --priority-max: %v", err)
		}
		in.priorityMax = p
		in.priorityMaxSet = true
	}

	in.pinnedFlag, _ = cmd.Flags().GetBool("pinned")
	in.noPinnedFlag, _ = cmd.Flags().GetBool("no-pinned")
	if in.pinnedFlag && in.noPinnedFlag {
		return in, HandleError("--pinned and --no-pinned are mutually exclusive")
	}

	in.includeTemplates, _ = cmd.Flags().GetBool("include-templates")
	in.includeGates, _ = cmd.Flags().GetBool("include-gates")
	in.includeInfra, _ = cmd.Flags().GetBool("include-infra")
	in.excludeTypeStrs, _ = cmd.Flags().GetStringSlice("exclude-type")

	in.parentID, _ = cmd.Flags().GetString("parent")
	if in.parentID == "" {
		in.parentID, _ = cmd.Flags().GetString("filter-parent")
	}
	in.noParent, _ = cmd.Flags().GetBool("no-parent")
	if in.parentID != "" && in.noParent {
		return in, HandleError("--parent and --no-parent are mutually exclusive")
	}

	if s, _ := cmd.Flags().GetString("mol-type"); s != "" {
		mt := types.MolType(s)
		if !mt.IsValid() {
			return in, HandleError("invalid mol-type %q (must be swarm, patrol, or work)", s)
		}
		in.molType = &mt
	}
	if s, _ := cmd.Flags().GetString("wisp-type"); s != "" {
		wt := types.WispType(s)
		if !wt.IsValid() {
			return in, HandleError("invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery, error, or escalation)", s)
		}
		in.wispType = &wt
	}

	in.deferredFlag, _ = cmd.Flags().GetBool("deferred")
	in.overdueFlag, _ = cmd.Flags().GetBool("overdue")

	var err error
	if in.createdAfter, err = parseListTimeFlag(cmd, "created-after"); err != nil {
		return in, err
	}
	if in.createdBefore, err = parseListTimeFlag(cmd, "created-before"); err != nil {
		return in, err
	}
	if in.updatedAfter, err = parseListTimeFlag(cmd, "updated-after"); err != nil {
		return in, err
	}
	if in.updatedBefore, err = parseListTimeFlag(cmd, "updated-before"); err != nil {
		return in, err
	}
	if in.closedAfter, err = parseListTimeFlag(cmd, "closed-after"); err != nil {
		return in, err
	}
	if in.closedBefore, err = parseListTimeFlag(cmd, "closed-before"); err != nil {
		return in, err
	}
	if in.deferAfter, err = parseListTimeFlag(cmd, "defer-after"); err != nil {
		return in, err
	}
	if in.deferBefore, err = parseListTimeFlag(cmd, "defer-before"); err != nil {
		return in, err
	}
	if in.dueAfter, err = parseListTimeFlag(cmd, "due-after"); err != nil {
		return in, err
	}
	if in.dueBefore, err = parseListTimeFlag(cmd, "due-before"); err != nil {
		return in, err
	}

	metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
	if len(metadataFieldFlags) > 0 {
		in.metadataFields = make(map[string]string, len(metadataFieldFlags))
		for _, mf := range metadataFieldFlags {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return in, HandleErrorRespectJSON("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return in, HandleErrorRespectJSON("invalid --metadata-field key: %v", err)
			}
			in.metadataFields[k] = v
		}
	}
	if k, _ := cmd.Flags().GetString("has-metadata-key"); k != "" {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return in, HandleErrorRespectJSON("invalid --has-metadata-key: %v", err)
		}
		in.hasMetadataKey = k
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
	in.readyFlag, _ = cmd.Flags().GetBool("ready")

	if in.sortBy != "" {
		validSortFields := map[string]bool{
			"priority": true, "created": true, "updated": true, "closed": true,
			"status": true, "id": true, "title": true, "type": true, "assignee": true,
		}
		if !validSortFields[in.sortBy] {
			return in, HandleError("invalid sort field %q (valid: priority, created, updated, closed, status, id, title, type, assignee)", in.sortBy)
		}
	}

	in.labels = utils.NormalizeLabels(in.labels)
	in.labelsAny = utils.NormalizeLabels(in.labelsAny)
	in.excludeLabels = utils.NormalizeLabels(in.excludeLabels)

	if !in.skipLabels && len(in.labels) == 0 && len(in.labelsAny) == 0 {
		if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
			in.labelsAny = dirLabels
		}
	}

	in.effectiveLimit = limit
	switch {
	case in.limitChanged:
		in.effectiveLimit = limit
	case in.allFlag:
		in.effectiveLimit = 0
	case !ui.IsTerminal():
		in.effectiveLimit = 0 // Piped stdout should not truncate (GH#4094)
	case ui.IsAgentMode():
		in.effectiveLimit = 20
	}
	in.sqlLimit = in.effectiveLimit
	// --sort id requires natural-numeric comparison (bd-9 < bd-10) that
	// SQL can't express without a schema-side sort column. Fall back to
	// fetching everything and sorting client-side. Other sorts (including
	// title via LOWER()) are pushed into SQL ORDER BY.
	if in.sortBy == "id" {
		in.sqlLimit = 0
	}

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
		if offset > 0 && in.sqlLimit == 0 && in.sortBy == "id" {
			return in, HandleError("--offset is not supported with --sort %s (sort requires fetching the full result set)", in.sortBy)
		}
		in.offset = offset
	}

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
